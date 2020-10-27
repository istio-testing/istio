//  Copyright Istio Authors
//
//  Licensed under the Apache License, Version 2.0 (the "License");
//  you may not use this file except in compliance with the License.
//  You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
//  Unless required by applicable law or agreed to in writing, software
//  distributed under the License is distributed on an "AS IS" BASIS,
//  WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
//  See the License for the specific language governing permissions and
//  limitations under the License.

package helm

import (
	"fmt"
	"path/filepath"

	"istio.io/istio/pkg/test/scopes"
	"istio.io/istio/pkg/test/shell"
)

// Helm allows clients to interact with helm commands in their cluster
type Helm struct {
	kubeConfig string
	baseDir    string
}

// NewHelm returns a new instance of a helm object.
func NewHelm(kubeConfig string) *Helm {
	return &Helm{
		kubeConfig: kubeConfig,
	}
}

// InstallChart installs the specified chart with its given name to the given namespace
func (h *Helm) InstallChart(name, relpath, namespace, overridesFile string) error {
	p := filepath.Join(h.baseDir, relpath)
	command := fmt.Sprintf("helm install %s %s --namespace %s -f %s --kubeconfig %s", name, p, namespace, overridesFile, h.kubeConfig)
	return execCommand(command)
}

// DeleteChart deletes the specified chart with its given name in the given namespace
func (h *Helm) DeleteChart(name, namespace string) error {
	command := fmt.Sprintf("helm delete %s --namespace %s --kubeconfig %s", name, namespace, h.kubeConfig)
	return execCommand(command)
}

func execCommand(cmd string) error {
	scopes.CI.Infof("Applying helm command: %s", cmd)

	s, err := shell.Execute(true, cmd)
	if err != nil {
		scopes.CI.Infof("(FAILED) Executing helm: %s (err: %v): %s", cmd, err, s)
		return fmt.Errorf("%v: %s", err, s)
	}

	return nil
}
