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
	"os/user"
	"path/filepath"
	"text/template"

	"github.com/gravitational/teleport/lib/client/db/profile"

	"github.com/gravitational/trace"
)

// TODO stub all this til we understand if it's relevant

type ServiceFile struct {
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

// LoadFromPath loads Posrtgres connection service file from the specified path.
func LoadFromPath(path string) (*ServiceFile, error) {
	return &ServiceFile{
		path:    path,
	}, nil
}

// Upsert adds the provided connection profile to the service file and saves it.
func (s *ServiceFile) Upsert(profile profile.ConnectProfile) error {
	return nil
}

// Env returns the specified connection profile information as a set of
// environment variables
func (s *ServiceFile) Env(serviceName string) (map[string]string, error) {
	env := map[string]string{
	}
	return env, nil
}

// Delete deletes the specified connection profile and saves the service file.
func (s *ServiceFile) Delete(name string) error {
	return nil
}

const (
	// serviceFile is the default name of the Postgres service file.
	serviceFile = ".todo.nonsuch.file.in.mongodb"
)

// Message is printed after MongoDB service file has been updated.
// TODO get this right!
var Message = template.Must(template.New("").Parse(`
Connection information for MongoDB database "{{.Name}}" has been saved.

You can now connect to the database using the following command:

  $ mongo {{if not .User}} -u "<user>"{{end}}{{if not .Database}} "<dbname>"{{end}}
`))
