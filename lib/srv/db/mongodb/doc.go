/*
Copyright 2020 Gravitational, Inc.

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

// stuff happens
// https://docs.mongodb.com/manual/reference/mongodb-wire-protocol/

package mongodb
/*
import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/mongo/description"
	"go.mongodb.org/mongo-driver/x/mongo/driver/topology"
	"go.mongodb.org/mongo-driver/x/mongo/driver/connstring"
)

cs, err := connstring.ParseAndValidate("mongodb://localhost:27017/")
cs.SSL = true
cs.SSLSet = true
cs.SSLInsecure = true
cs.SSLInsecureSet = true
cs.SSLClientCertificateKeyFile = "me.cert.and.key.pem"
cs.SSLClientCertificateKeyFileSet = true

t, err := topology.New(
	topology.WithConnString(
		func (connstring.ConnString) connstring.ConnString { return cs }
	)
)

err := t.Connect()
ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
srv, err := t.SelectServer(ctx, description.WriteSelector())
conn, err := srv.Connection(ctx)
conn.WriteWireMessage(ctx, []byte{4,0,0,0})
conn.Close()
t.Disconnect(ctx)
*/
