/*
Copyright 2020-2021 Gravitational, Inc.

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

package mongodb

import (
	"context"
	"crypto/tls"
	"io/ioutil"
	"net"

	"github.com/gravitational/teleport/lib/services"
	"github.com/gravitational/teleport/lib/srv/db/common"

	"go.mongodb.org/mongo-driver/mongo/description"
	"go.mongodb.org/mongo-driver/x/mongo/driver"
	"go.mongodb.org/mongo-driver/x/mongo/driver/connstring"
	"go.mongodb.org/mongo-driver/x/mongo/driver/topology"

	"github.com/gravitational/trace"
	"github.com/jonboulle/clockwork"
	"github.com/sirupsen/logrus"
)

// Engine implements the Postgres database service that accepts client
// connections coming over reverse tunnel from the proxy and proxies
// them between the proxy and the Postgres database instance.
//
// Implements common.Engine.
type Engine struct {
	// Auth handles database access authentication.
	Auth *common.Auth
	// Audit emits database access audit events.
	Audit *common.Audit
	// Context is the database server close context.
	Context context.Context
	// Clock is the clock interface.
	Clock clockwork.Clock
	// Log is used for logging.
	Log logrus.FieldLogger
}

// HandleConnection processes the connection from Postgres proxy coming
// over reverse tunnel.
//
// It handles all necessary startup actions, authorization and acts as a
// middleman between the proxy and the database intercepting and interpreting
// all messages i.e. doing protocol parsing.
func (e *Engine) HandleConnection(ctx context.Context, sessionCtx *common.Session, clientConn net.Conn) (err error) {
	defer func() {
		if err != nil {
			e.Log.WithError(err).Error("Failed to send error to client.")
		}
	}()
	// TODO something about auth???
	err = e.handleStartup(sessionCtx)
	if err != nil {
		return trace.Wrap(err)
	}
	// Now we know which database/username the user is connecting to, so
	// perform an authorization check.
	err = e.checkAccess(sessionCtx)
	if err != nil {
		return trace.Wrap(err)
	}
	e.Log.Debug("Initialised, opening connection ...")
	// This is where we connect to the actual MongoDB database.
	serverConn, err := e.connect(ctx, sessionCtx)
	if err != nil {
		return trace.Wrap(err)
	}
	e.Log.Debug("Connection open!")
	// At this point the client should be ready to start sending
	// messages: this is where the prompt appears on the other side.
	err = e.Audit.OnSessionStart(e.Context, *sessionCtx, nil)
	if err != nil {
		return trace.Wrap(err)
	}
	defer func() {
		err := e.Audit.OnSessionEnd(e.Context, *sessionCtx)
		if err != nil {
			e.Log.WithError(err).Error("Failed to emit audit event.")
		}
	}()
	// Now launch the message exchange relaying all intercepted messages b/w
	// the client and the server (database).
	clientErrCh := make(chan error, 1)
	serverErrCh := make(chan error, 1)
	go e.receiveFromClient(clientConn, serverConn, clientErrCh, ctx, sessionCtx)
	go e.receiveFromServer(serverConn, clientConn, serverErrCh, ctx, sessionCtx)
	select {
	case err := <-clientErrCh:
		e.Log.WithError(err).Debug("Client done.")
	case err := <-serverErrCh:
		e.Log.WithError(err).Debug("Server done.")
	case <-ctx.Done():
		e.Log.Debug("Context canceled.")
	}
	return nil
}

// handleStartup updates the session context with the connection parameters.
func (e *Engine) handleStartup(sessionCtx *common.Session) error {

	sessionCtx.DatabaseName = "admin"
	sessionCtx.DatabaseUser = "testuser"
	return nil
}

func (e *Engine) checkAccess(sessionCtx *common.Session) error {
	ap, err := e.Auth.GetAuthPreference()
	if err != nil {
		return trace.Wrap(err)
	}
	mfaParams := services.AccessMFAParams{
		Verified:       sessionCtx.Identity.MFAVerified != "",
		AlwaysRequired: ap.GetRequireSessionMFA(),
	}
	err = sessionCtx.Checker.CheckAccessToDatabase(sessionCtx.Server, mfaParams,
		&services.DatabaseLabelsMatcher{Labels: sessionCtx.Server.GetAllLabels()},
		&services.DatabaseUserMatcher{User: sessionCtx.DatabaseUser},
		&services.DatabaseNameMatcher{Name: sessionCtx.DatabaseName})
	if err != nil {
		if err := e.Audit.OnSessionStart(e.Context, *sessionCtx, err); err != nil {
			e.Log.WithError(err).Error("Failed to emit audit event.")
		}
		return trace.Wrap(err)
	}
	return nil
}

// connect establishes the connection to the database instance and returns
// the connection
func (e *Engine) connect(ctx context.Context, sessionCtx *common.Session) (driver.Connection, error) {
	// parse a connection string into meaningful data, including getting
	// tls setup from teleport's CA
	connectConfig, err := e.getConnectConfig(ctx, sessionCtx)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	e.Log.Debug(" ... have connection config")
	// create a topology that reflects the mongodb cluster we're connecting to
	top, err := topology.New(connectConfig...)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	e.Log.Debug(" ... have topology")
	err = top.Connect()
	if err != nil {
		return nil, trace.Wrap(err)
	}
	defer top.Disconnect(ctx)
	e.Log.Debug(" ... topology connected")

	// find a server in the topology to connect to
	srv, err := top.SelectServer(ctx, description.WriteSelector())
	if err != nil {
		return nil, trace.Wrap(err)
	}
	e.Log.Debug(" ... server selected")

	// connect to the server
	conn, err := srv.Connection(ctx)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	defer conn.Close()

	return conn, nil
}

// receiveFromClient receives messages from the provided backend (which
// in turn receives them from psql or other client) and relays them to
// the frontend connected to the database instance.
func (e *Engine) receiveFromClient(clientConn net.Conn, serverConn driver.Connection, clientErrCh chan<- error, ctx context.Context, sessionCtx *common.Session) {
	log := e.Log.WithFields(logrus.Fields{
		"from":   "client",
		"client": clientConn.RemoteAddr(),
		"server": serverConn.Address(),
	})
	defer log.Debug("Stop receiving from client.")
	for {
		// TODO use the wire format to know how much to read
		message, err := ioutil.ReadAll(clientConn)
		if err != nil {
			log.WithError(err).Errorf("Failed to receive message from client.")
			clientErrCh <- err
			return
		}
		log.Debugf("Received client data: %#v.", message)

		// TODO USEFUL AUDITING HERE!!!

		err = serverConn.WriteWireMessage(ctx, message)
		if err != nil {
			log.WithError(err).Error("Failed to send message to server.")
			clientErrCh <- err
			return
		}
	}
}

// receiveFromServer receives messages from the provided frontend (which
// is connected to the database instance) and relays them back to the psql
// or other client via the provided backend.
func (e *Engine) receiveFromServer(serverConn driver.Connection, clientConn net.Conn, serverErrCh chan<- error, ctx context.Context, sessionCtx *common.Session) {
	log := e.Log.WithFields(logrus.Fields{
		"from":   "server",
		"client": clientConn.RemoteAddr(),
		"server": serverConn.Address(),
	})
	defer log.Debug("Stop receiving from server.")
	for {
		var message []byte
		message, err := serverConn.ReadWireMessage(ctx, message)
		if err != nil {
			log.WithError(err).Errorf("Failed to receive message from server.")
			serverErrCh <- err
			return
		}
		log.Debugf("Received server data: %#v.", message)

		// TODO useful auditing

		_, err = clientConn.Write(message)
		if err != nil {
			log.WithError(err).Error("Failed to send message to client.")
			serverErrCh <- err
			return
		}
	}
}

// getConnectConfig returns config that can be used to connect to the
// database instance.
func (e *Engine) getConnectConfig(ctx context.Context, sessionCtx *common.Session) ([]topology.Option, error) {
	// The driver requires the config to be built by parsing the connection
	// string so parse the basic template and then fill in the rest of
	// parameters such as TLS configuration.
	cs, err := connstring.ParseAndValidate("mongodb://localhost:27017/?serverSelectionTimeoutMS=500")
	if err != nil {
		return nil, trace.Wrap(err)
	}

	// turn off OCSP
	cs.SSLDisableOCSPEndpointCheck = true
	cs.SSLDisableOCSPEndpointCheckSet = true

	var opts []topology.Option

	opts = append(opts, topology.WithConnString(func (connstring.ConnString) connstring.ConnString {
		return cs
	}))

	// TLS config will use client certificate for an onprem database or
	// will contain RDS root certificate for RDS/Aurora.
	tlsConfig, err := e.Auth.GetTLSConfig(ctx, sessionCtx)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	tlsOpts := topology.WithTLSConfig(func(*tls.Config) *tls.Config {
		return tlsConfig
	})

	whyOpts := topology.WithConnectionOptions(func(o ...topology.ConnectionOption) []topology.ConnectionOption {
		return append(o, tlsOpts)
	})
	whyWhyOpts := topology.WithServerOptions(func(o ...topology.ServerOption) []topology.ServerOption {
		return append(o, whyOpts)
	})

	return append(opts, whyWhyOpts), nil
}
