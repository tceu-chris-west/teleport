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
	"net"

	"github.com/gravitational/teleport/lib/auth"
	"github.com/gravitational/teleport/lib/srv/db/common"

	"github.com/gravitational/trace"
	"github.com/sirupsen/logrus"
)

// Proxy proxies connections from Postgres clients to database services
// over reverse tunnel. It runs inside Teleport proxy service.
//
// Implements common.Proxy.
type Proxy struct {
	// TLSConfig is the proxy TLS configuration.
	TLSConfig *tls.Config
	// Middleware is the auth middleware.
	Middleware *auth.Middleware
	// Service is used to connect to a remote database service.
	Service common.Service
	// Log is used for logging.
	Log logrus.FieldLogger
}

// HandleConnection accepts connection from a MongoDB client, authenticates
// it and proxies it to an appropriate database service.
func (p *Proxy) HandleConnection(ctx context.Context, clientConn net.Conn) (err error) {
	tlsConn := tls.Server(clientConn, p.TLSConfig)
	defer tlsConn.Close()
	// this is necessary so that we can read the identity from the client
	err = tlsConn.Handshake()
	if err != nil {
		return trace.Wrap(err)
	}

	ctx, err = p.Middleware.WrapContextWithUser(ctx, tlsConn)
	if err != nil {
		return trace.Wrap(err)
	}
	serviceConn, err := p.Service.Connect(ctx, "", "")
	if err != nil {
		return trace.Wrap(err)
	}
	defer serviceConn.Close()

	err = p.Service.Proxy(ctx, tlsConn, serviceConn)
	if err != nil {
		return trace.Wrap(err)
	}
	return nil
}
