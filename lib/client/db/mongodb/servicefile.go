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
	"fmt"
	"strconv"
	"strings"
	"os/user"
	"path/filepath"
	"text/template"

	"github.com/gravitational/teleport/lib/client/db/profile"

	"github.com/gravitational/trace"
	"gopkg.in/ini.v1"
)

type ServiceFile struct {
	// iniFile is the underlying ini file.
	iniFile *ini.File
	// path is the service file path.
	path string
}

func Load() (*ServiceFile, error) {
	user, err := user.Current()
	if err != nil {
		return nil, trace.ConvertSystemError(err)
	}
	return LoadFromPath(filepath.Join(user.HomeDir, serviceFile))
}

// LoadFromPath loads MongoDB connection service file from the specified path.
func LoadFromPath(path string) (*ServiceFile, error) {
	// Loose load will ignore file not found error.
	iniFile, err := ini.LooseLoad(path)
	if err != nil {
		return nil, trace.Wrap(err)
	}
	return &ServiceFile{
		iniFile: iniFile,
		path:    path,
	}, nil
}

// Upsert adds the provided connection profile to the service file and saves it.
func (s *ServiceFile) Upsert(profile profile.ConnectProfile) error {
	section := s.iniFile.Section(profile.Name)
	if section != nil {
		s.iniFile.DeleteSection(profile.Name)
	}
	section, err := s.iniFile.NewSection(profile.Name)
	if err != nil {
		return trace.Wrap(err)
	}
	section.NewKey("host", profile.Host)
	section.NewKey("port", strconv.Itoa(profile.Port))
	if profile.User != "" {
		section.NewKey("user", profile.User)
	}
	if profile.Database != "" {
		section.NewKey("dbname", profile.Database)
	}
	section.NewKey("sslinsecure", strconv.FormatBool(profile.Insecure))
	section.NewKey("sslrootcert", profile.CACertPath)
	section.NewKey("sslcertkey", profile.CertAndKey)
	ini.PrettyFormat = false // Pretty format breaks stuff
	return s.iniFile.SaveTo(s.path)
}

// Env returns the specified connection profile information as a set of
// environment variables
func (s *ServiceFile) Env(serviceName string) (map[string]string, error) {
	section, err := s.iniFile.GetSection(serviceName)
	if err != nil {
		if strings.Contains(err.Error(), "does not exist") {
			return nil, trace.NotFound("service connection profile %q not found", serviceName)
		}
		return nil, trace.Wrap(err)
	}
	host, err := section.GetKey("host")
	if err != nil {
		return nil, trace.Wrap(err)
	}
	port, err := section.GetKey("port")
	if err != nil {
		return nil, trace.Wrap(err)
	}
	sslRootCert, err := section.GetKey("sslrootcert")
	if err != nil {
		return nil, trace.Wrap(err)
	}
	sslCertKey, err := section.GetKey("sslcertkey")
	if err != nil {
		return nil, trace.Wrap(err)
	}
	sslInsecureParam := ""
	sslInsecureArg := ""
	sslInsecure, err := section.GetKey("sslinsecure")
	if err != nil {
		return nil, trace.Wrap(err)
	}
	if val, _ := sslInsecure.Bool(); val {
		sslInsecureParam = " --tlsAllowInvalidCertificates"
		sslInsecureArg = "&tlsAllowInvalidCertificates=true"
	}

	env := map[string]string{
		"MONGO_PARAMS": fmt.Sprintf(`"mongodb://%s:%s/ --tls --tlsCAFile %s --tlsCertificateKeyFile %s --authenticationDatabase '\$external' --authenticationMechanism MONGODB-X509%s"`, host, port, sslRootCert, sslCertKey, sslInsecureParam),
		"MONGO_CONN": fmt.Sprintf(`"mongodb://%s:%s/?tls=true&tlsCAFile=%s&tlsCertificateKeyFile=%s&authenticationDatabase=\$external&authenticationMethod=MONGODB-X509%s"`, host, port, sslRootCert, sslCertKey, sslInsecureArg),
	}
	return env, nil
}

// Delete deletes the specified connection profile and saves the service file.
func (s *ServiceFile) Delete(name string) error {
	s.iniFile.DeleteSection(name)
	return s.iniFile.SaveTo(s.path)
}

const (
	// serviceFile is the default name of the MongoDB service file.
	serviceFile = ".tsh/mongodb.conf"
)

// Message is printed after MongoDB service file has been updated.
var Message = template.Must(template.New("").Parse(`
Connection information for MongoDB database "{{.Name}}" has been saved.
You can now connect to the database using the following command:

  $ mongo mongodb://{{.Host}}:{{.Port}}/ --tls --tlsCAFile {{.CACertPath}} --tlsCertificateKeyFile {{.CertAndKey}} --authenticationDatabase '$external' --authenticationMechanism MONGODB-X509

... or ...

  $ mongosh "mongodb://{{.Host}}:{{.Port}}/?tls=true&tlsCAFile={{.CACertPath}}&tlsCertificateKeyFile={{.CertAndKey}}&authenticationDatabase=\$external&authenticationMethod=MONGODB-X509"

... or more succinctly ...

 $ eval $(tsh db env)  # ... will set $MONGO_PARAMS and $MONGO_CONN
 $ echo $MONGO_PARAMS | xargs -o mongo  # ... connect using mongo
 $ mongosh $MONGO_CONN  # .. connect using mongosh 

You might need --tlsAllowInvalidCertificates if the proxy's certificate has a
very long expiry times. This option will be set automatically when using
"tsh --insecure db login" and "tsh db env".
`))
