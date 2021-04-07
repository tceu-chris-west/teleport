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

package mongodb

/*
Wire protocol documentation:
  https://docs.mongodb.com/manual/reference/mongodb-wire-protocol/

OP_MSG documentation:
  https://github.com/mongodb/specifications/blob/master/source/message/OP_MSG.rst

mongo client  ---(TLS)-->  proxy  ---(TLS)-->  mongodb

  OP_MSG(authenticate) ------X------------------->


The mongo client sends an "authenticate" OP_MSG across once the TLS handshake
is done. This contains metadata from the client certificate presented to the
proxy for authentication, and requires some tampering with at the proxy so that
the CN of the user in the authenticate message matches the certificate presented
by the proxy to the database. Furthermore, X509-authenticated MongoDB usernames
must *match* the CN of the presented certificate, so Teleport-issued certs are
no good as MongoDB identities.

To compensate for this, the config will use the "user" tag as the CN in the
client cert presented by the proxy, if it's present. e.g. (in teleport.yml)

db_service:
  enabled: "yes"
  databases:
   - name: testdb
     protocol: mongodb
     uri: my.test.database:27017
     static_labels:
       # presents a cert with CN=my.username
       # expects a user to exist in mongodb named 'CN=my.username'
       user: my.username

*/
