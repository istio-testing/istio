// Copyright Istio Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package mesh

import (
	"github.com/spf13/cobra"

	"istio.io/istio/operator/pkg/util/clog"
	buildversion "istio.io/pkg/version"
)

type operatorDumpArgs struct {
	// common is shared operator args
	common operatorCommonArgs
}

func addOperatorDumpFlags(cmd *cobra.Command, args *operatorDumpArgs) {
	hub, tag := buildversion.DockerInfo.Hub, buildversion.DockerInfo.Tag

	cmd.PersistentFlags().StringVar(&args.common.hub, "hub", hub, HubFlagHelpStr)
	cmd.PersistentFlags().StringVar(&args.common.tag, "tag", tag, TagFlagHelpStr)
	cmd.PersistentFlags().StringVar(&args.common.imagePullSecrets, "imagePullSecrets", "", ImagePullSecretsHelpStr)
	cmd.PersistentFlags().StringVar(&args.common.operatorNamespace, "operatorNamespace", operatorDefaultNamespace, OperatorNamespaceHelpstr)
	cmd.PersistentFlags().StringVar(&args.common.istioNamespace, "istioNamespace", istioDefaultNamespace,
		"The namespace Istio is installed into")
	cmd.PersistentFlags().StringVarP(&args.common.manifestsPath, "charts", "", "", ChartsDeprecatedStr)
	cmd.PersistentFlags().StringVarP(&args.common.manifestsPath, "manifests", "d", "", ManifestsFlagHelpStr)
	cmd.PersistentFlags().StringVarP(&args.common.revision, "revision", "r", "",
		revisionFlagHelpStr)
}

func operatorDumpCmd(rootArgs *rootArgs, odArgs *operatorDumpArgs) *cobra.Command {
	return &cobra.Command{
		Use:   "dump",
		Short: "Dumps the Istio operator controller manifest.",
		Long:  "The dump subcommand dumps the Istio operator controller manifest.",
		Args:  cobra.ExactArgs(0),
		Run: func(cmd *cobra.Command, args []string) {
			l := clog.NewConsoleLogger(cmd.OutOrStdout(), cmd.ErrOrStderr(), installerScope)
			operatorDump(rootArgs, odArgs, l)
		}}
}

// operatorDump dumps the manifest used to install the operator.
func operatorDump(args *rootArgs, odArgs *operatorDumpArgs, l clog.Logger) {
	_, mstr, err := renderOperatorManifest(args, &odArgs.common)
	if err != nil {
		l.LogAndFatal(err)
	}

	l.Print(mstr)
}
