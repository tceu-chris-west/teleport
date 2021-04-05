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
	"bufio"
	"context"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"net"

	"go.mongodb.org/mongo-driver/bson"

	"github.com/gravitational/teleport/lib/services"
	"github.com/gravitational/teleport/lib/srv/db/common"

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
	// get user/db to connect _to_ on remote
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
	defer serverConn.Close()

	e.Log.Debug("Handshaking with db ...")
	err = serverConn.Handshake()
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
	go e.receiveFromClient(clientConn, *serverConn, clientErrCh, ctx, sessionCtx)
	go e.receiveFromServer(*serverConn, clientConn, serverErrCh, ctx, sessionCtx)
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
	// this'll get overwritten by $db in OP_MSG
	sessionCtx.DatabaseName = "[mongodb]"
	// take user from labels if set there; otherwise from client
	if val, ok := sessionCtx.Server.GetAllLabels()["user"]; ok {
		sessionCtx.DatabaseUser = val
	} else {
		sessionCtx.DatabaseUser = sessionCtx.Identity.Username
	}
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
func (e *Engine) connect(ctx context.Context, sessionCtx *common.Session) (*tls.Conn, error) {
	tlsConfig, err := e.Auth.GetTLSConfig(ctx, sessionCtx)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return tls.Dial("tcp", sessionCtx.Server.GetURI(), tlsConfig)
}

// receiveFromClient receives messages from the provided backend (which
// in turn receives them from psql or other client) and relays them to
// the frontend connected to the database instance.
//func (e *Engine) receiveFromClient(clientConn net.Conn, serverConn driver.Connection, clientErrCh chan<- error, ctx context.Context, sessionCtx *common.Session) {
func (e *Engine) receiveFromClient(clientConn net.Conn, serverConn tls.Conn, clientErrCh chan<- error, ctx context.Context, sessionCtx *common.Session) {
	log := e.Log.WithFields(logrus.Fields{
		"from":   "client",
		"client": clientConn.RemoteAddr(),
		"server": serverConn.RemoteAddr(),
	})
	defer log.Debug("Stop receiving from client.")

	c := bufio.NewReader(clientConn)
	for {
		// use the wire format to understand how much to read
		mbuf, err := c.Peek(4)
		if err != nil {
			log.WithError(err).Errorf("Failed to receive message length from client.")
			clientErrCh <- err
			return
		}
		mlen := binary.LittleEndian.Uint32(mbuf)
		message := make([]byte, mlen)
		rlen, err := io.ReadFull(c, message)
		if err != nil {
			log.WithError(err).Errorf("Failed to receive message data from client.")
			clientErrCh <- err
			return
		}
		if uint32(rlen) != mlen {
			// TODO this ... can't happen??
			log.WithError(err).Errorf("Failed to receive whole message from client.")
			clientErrCh <- err
			return
		}

		log.Debugf("Received client data: %#v.", message)

		auditMessage, _ := e.parseMessage(message, sessionCtx)
		if auditMessage != "" {
			if err := e.Audit.OnQuery(e.Context, *sessionCtx, auditMessage); err != nil {
				log.WithError(err).Error("Failed to emit audit event.")
			}
			// TODO this is nasty stuff -- truncate the message if
			//      parseMessage() changed it. parseX() shouldn't
			//      mutate X :(((
			mlen = binary.LittleEndian.Uint32(message[:4])
			message = message[:mlen]
		}

		_, err = serverConn.Write(message)
		if err != nil {
			log.WithError(err).Error("Failed to send message to server.")
			clientErrCh <- err
			return
		}
	}
}

func (e *Engine) parseMessage(message []byte, sessionCtx *common.Session) (string, error) {
	switch int(message[13]) << 8 + int(message[12]) {
		case 2013: // OP_MSG
			if message[20] != 0x0 {
				// TODO handle other section types
				e.Log.Debugf("Will not audit OP_MSG with section %#v", message[20])
				return "", nil
			}
			m := map[string]interface{}{}
			err := bson.Unmarshal(message[21:], &m)
			if err != nil {
				e.Log.WithError(err).Warn("Failed to parse OP_MSG BSON data")
				return "", err
			}
			if _, ok := m["authenticate"]; ok {
				// stop the client from overwriting the cert
				var authData = AuthenticateBson{}
				_ = bson.Unmarshal(message[21:], &authData)
				authData.User = fmt.Sprintf("CN=%s", sessionCtx.DatabaseUser)
				authDataBytes, _ := bson.Marshal(authData)
				copy(message[21:], authDataBytes)
				binary.LittleEndian.PutUint32(message, uint32(len(authDataBytes) + 21))
				e.Log.Debugf("Mangled client message to: %#v.", message)
			}
			if db, ok := m["$db"]; ok {
				sessionCtx.DatabaseName = fmt.Sprintf("%s", db)
			}
			return fmt.Sprintf("%#v", m), nil
		// TODO handle OP_QUERY
	}
	return "", nil
}

type AuthenticateBson struct{
	Authenticate int     `bson:"authenticate"`
	Mechanism    string  `bson:"mechanism"`
	User         string  `bson:"user"`
	DB           string  `bson:"$db"`
}

// receiveFromServer receives messages from the provided frontend (which
// is connected to the database instance) and relays them back to the psql
// or other client via the provided backend.
//func (e *Engine) receiveFromServer(serverConn driver.Connection, clientConn net.Conn, serverErrCh chan<- error, ctx context.Context, sessionCtx *common.Session) {
func (e *Engine) receiveFromServer(serverConn tls.Conn, clientConn net.Conn, serverErrCh chan<- error, ctx context.Context, sessionCtx *common.Session) {
	log := e.Log.WithFields(logrus.Fields{
		"from":   "server",
		"client": clientConn.RemoteAddr(),
		"server": serverConn.RemoteAddr(),
	})
	defer log.Debug("Stop receiving from server.")

	c := bufio.NewReader(&serverConn)
	for {
		mbuf, err := c.Peek(4)
		if err != nil {
			log.WithError(err).Errorf("Failed to receive message length from server.")
			serverErrCh <- err
			return
		}
		mlen := binary.LittleEndian.Uint32(mbuf)
		message := make([]byte, mlen)
		rlen, err := io.ReadFull(c, message)
		if err != nil {
			log.WithError(err).Errorf("Failed to receive message data from server.")
			serverErrCh <- err
			return
		}
		if uint32(rlen) != mlen {
			// TODO this ... can't happen??
			log.WithError(err).Errorf("Failed to receive whole message from server.")
			serverErrCh <- err
			return
		}

		log.Debugf("Received server data: %#v.", message)
		_, err = clientConn.Write(message)
		if err != nil {
			log.WithError(err).Error("Failed to send message to client.")
			serverErrCh <- err
			return
		}
	}
}
