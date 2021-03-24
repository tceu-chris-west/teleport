/*
Copyright 2021 Gravitational, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package db

import (
	"context"
	"testing"
	"time"

	"github.com/gravitational/teleport/api/types"
	"github.com/gravitational/teleport/api/types/events"
	"github.com/gravitational/teleport/lib/auth"
	libevents "github.com/gravitational/teleport/lib/events"

	"github.com/gravitational/trace"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

// TestAuditPostgres verifies proper audit events are emitted for Postgres
// connections.
func TestAuditPostgres(t *testing.T) {
	ctx := context.Background()
	testCtx := setupTestContext(ctx, t)
	t.Cleanup(func() { testCtx.Close() })
	go testCtx.startHandlingPostgresConnections()

	_, role, err := auth.CreateUserAndRole(testCtx.tlsServer.Auth(), "alice", []string{"admin"})
	require.NoError(t, err)

	role.SetDatabaseNames(types.Allow, []string{"postgres"})
	role.SetDatabaseUsers(types.Allow, []string{"postgres"})
	err = testCtx.tlsServer.Auth().UpsertRole(ctx, role)
	require.NoError(t, err)

	// Access denied should trigger an unsuccessful session start event.
	_, err = testCtx.postgresClient(ctx, "alice", "notpostgres", "notpostgres")
	require.Error(t, err)
	select {
	case event := <-testCtx.emitter.eventsCh:
		require.Equal(t, libevents.DatabaseSessionStartFailureCode, event.GetCode())
	case <-time.After(time.Second):
		t.Fatalf("didn't receive %v event after 1 second", libevents.DatabaseSessionStartFailureCode)
	}

	// Connect should trigger successful session start event.
	psql, err := testCtx.postgresClient(ctx, "alice", "postgres", "postgres")
	require.NoError(t, err)
	select {
	case event := <-testCtx.emitter.eventsCh:
		require.Equal(t, libevents.DatabaseSessionStartCode, event.GetCode())
	case <-time.After(time.Second):
		t.Fatalf("didn't receive %v event after 1 second", libevents.DatabaseSessionStartCode)
	}

	// Simple query should trigger the query event.
	_, err = psql.Exec(ctx, "select 1").ReadAll()
	require.NoError(t, err)
	select {
	case event := <-testCtx.emitter.eventsCh:
		require.Equal(t, libevents.DatabaseSessionQueryCode, event.GetCode())
	case <-time.After(time.Second):
		t.Fatalf("didn't receive %v event after 1 second", libevents.DatabaseSessionQueryCode)
	}

	// Prepared statement execution should also trigger a query event.
	result := psql.ExecParams(ctx, "select 1", nil, nil, nil, nil).Read()
	require.NoError(t, result.Err)
	select {
	case event := <-testCtx.emitter.eventsCh:
		require.Equal(t, libevents.DatabaseSessionQueryCode, event.GetCode())
	case <-time.After(time.Second):
		t.Fatalf("didn't receive %v event after 1 second", libevents.DatabaseSessionQueryCode)
	}

	// Closing connection should trigger session end event.
	err = psql.Close(ctx)
	require.NoError(t, err)
	select {
	case event := <-testCtx.emitter.eventsCh:
		require.Equal(t, libevents.DatabaseSessionEndCode, event.GetCode())
	case <-time.After(time.Second):
		t.Fatalf("didn't receive %v event after 1 second", libevents.DatabaseSessionEndCode)
	}
}

// TestAuditMySQL verifies proper audit events are emitted for MySQL
// connections.
func TestAuditMySQL(t *testing.T) {
	ctx := context.Background()
	testCtx := setupTestContext(ctx, t)
	t.Cleanup(func() { testCtx.Close() })
	go testCtx.startHandlingMySQLConnections()

	_, role, err := auth.CreateUserAndRole(testCtx.tlsServer.Auth(), "alice", []string{"admin"})
	require.NoError(t, err)

	role.SetDatabaseUsers(types.Allow, []string{"root"})
	err = testCtx.tlsServer.Auth().UpsertRole(ctx, role)
	require.NoError(t, err)

	// Access denied should trigger an unsuccessful session start event.
	_, err = testCtx.mysqlClient("alice", "notroot")
	require.Error(t, err)
	select {
	case event := <-testCtx.emitter.eventsCh:
		require.Equal(t, libevents.DatabaseSessionStartFailureCode, event.GetCode())
	case <-time.After(time.Second):
		t.Fatalf("didn't receive %v event after 1 second", libevents.DatabaseSessionStartFailureCode)
	}

	// Connect should trigger successful session start event.
	mysql, err := testCtx.mysqlClient("alice", "root")
	require.NoError(t, err)
	select {
	case event := <-testCtx.emitter.eventsCh:
		require.Equal(t, libevents.DatabaseSessionStartCode, event.GetCode())
	case <-time.After(time.Second):
		t.Fatalf("didn't receive %v event after 1 second", libevents.DatabaseSessionStartCode)
	}

	// Simple query should trigger the query event.
	_, err = mysql.Execute("select 1")
	require.NoError(t, err)
	select {
	case event := <-testCtx.emitter.eventsCh:
		require.Equal(t, libevents.DatabaseSessionQueryCode, event.GetCode())
	case <-time.After(time.Second):
		t.Fatalf("didn't receive %v event after 1 second", libevents.DatabaseSessionQueryCode)
	}

	// Closing connection should trigger session end event.
	err = mysql.Close()
	require.NoError(t, err)
	select {
	case event := <-testCtx.emitter.eventsCh:
		require.Equal(t, libevents.DatabaseSessionEndCode, event.GetCode())
	case <-time.After(time.Second):
		t.Fatalf("didn't receive %v event after 1 second", libevents.DatabaseSessionEndCode)
	}
}

// testEmitter pushes all received audit events into a channel.
type testEmitter struct {
	eventsCh chan events.AuditEvent
	log      logrus.FieldLogger
}

// newTestEmitter returns a new instance of test emitter.
func newTestEmitter() *testEmitter {
	return &testEmitter{
		eventsCh: make(chan events.AuditEvent, 100),
		log:      logrus.WithField(trace.Component, "emitter"),
	}
}

// EmitAuditEvent records the provided event in the test emitter.
func (e *testEmitter) EmitAuditEvent(ctx context.Context, event events.AuditEvent) error {
	e.log.Infof("EmitAuditEvent(%v)", event)
	e.eventsCh <- event
	return nil
}
