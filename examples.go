package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

type exampleFile struct {
	name    string
	format  string
	content string
}

type cliExample struct {
	title       string
	description string
	shell       string
	files       []exampleFile
}

type exampleSection struct {
	title       string
	description string
	examples    []cliExample
}

func newExamplesCommand() *cobra.Command {
	var colorMode string
	cmd := &cobra.Command{
		Use:     "examples",
		Aliases: []string{"example"},
		Short:   "Show practical port-forward and diagnostic gateway examples",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			color, err := examplesUseColor(cmd.OutOrStdout(), colorMode)
			if err != nil {
				return err
			}
			return writeExamples(cmd.OutOrStdout(), color)
		},
	}
	cmd.Flags().StringVar(&colorMode, "color", "auto", "colorize output: auto, always, never")
	return cmd
}

var examplesCatalog = []exampleSection{
	{
		title:       "Forward pods and services",
		description: "Replace the namespace, resource names, and ports with values from your cluster.",
		examples: []cliExample{
			{title: "Forward one Pod", shell: "kube-port-forward -n demo pod/api-7c9f4d8b6f-abcde 9100:9100"},
			{title: "Forward every Pod selected by a Service", description: "The remote port is a Pod port, not the Service port.", shell: "kube-port-forward -n demo svc/api 9100:8080"},
			{title: "Forward every declared container port", description: "The first local port is 10000; additional ports and replicas receive subsequent free ports.", shell: "kube-port-forward -n demo svc/api 10000:*"},
		},
	},
	{
		title:       "Discover resources",
		description: "Selectors are watched for Pod changes and Deployment rollouts.",
		examples: []cliExample{
			{title: "Forward all Pods in a Deployment", shell: "kube-port-forward -n demo deploy/api 10000:*"},
			{title: "Discover all Services in one namespace", description: "Quote the asterisk so your shell does not expand it. Services added after startup need a restart.", shell: "kube-port-forward -n demo 'svc/*' 10000:*"},
			{title: "Forward several resources in one process", shell: "kube-port-forward -n demo svc/api 9100:8080 deploy/worker 9200:9200"},
		},
	},
	{
		title:       "Connection and UI options",
		description: "The web UI normally opens at http://127.0.0.1:9999.",
		examples: []cliExample{
			{title: "Set retry limits and a different UI port", shell: "kube-port-forward -n demo --retries 5 --retry-interval 10 --web-port 9998 svc/api 9100:8080"},
			{title: "Run without opening the browser", shell: "kube-port-forward -n demo --no-open svc/api 9100:8080"},
			{title: "Use a cluster with an untrusted API certificate", description: "--insecure affects Kubernetes API TLS only; it does not control HTTPS target verification in the diagnostic UI.", shell: "kube-port-forward --insecure -n demo svc/api 9100:8080"},
		},
	},
	{
		title:       "Reach a cluster-only host",
		description: "Publish the proxy image to a registry your cluster can pull from, then create a local diagnostic profile.",
		examples: []cliExample{
			{
				title:       "Build and publish the proxy image",
				description: "Run this from the source repository. Choose --platform for your cluster nodes and use a registry they can access.",
				shell:       "docker buildx build --platform linux/amd64 -f Dockerfile.proxy -t registry.example.com/tools/kube-port-forward-proxy:latest --push .",
			},
			{
				title:       "Allow one HTTPS destination",
				description: "The proxy enforces this allowlist inside the cluster. Adjust the image and add a pull secret if your registry needs one.",
				files: []exampleFile{{name: "diagnostic.yaml", format: "YAML", content: `image: registry.example.com/tools/kube-port-forward-proxy:latest
pullPolicy: IfNotPresent
imagePullSecrets: []
ttl: 1h
policy:
  defaultAction: deny
  allow:
    - hosts:
        - metrics.internal.example.com
      ports:
        - "443"`}},
				shell: "kube-port-forward -n demo --diagnostic-config diagnostic.yaml",
			},
			{
				title:       "Expose the host from the web UI",
				description: "Click Start diagnostic pod. Add metrics.internal.example.com, target port 443, local port 18443, mode HTTPS. Upstream TLS verification is on by default; disable it per endpoint only if needed.",
				shell:       "curl 'http://127.0.0.1:18443/api/v1/query?query=up'",
			},
			{
				title:       "Use TCP passthrough instead",
				description: "Select TCP in the UI for end-to-end TLS or non-HTTP protocols. --resolve keeps the target hostname for TLS SNI while connecting to the local port.",
				shell:       "curl --resolve metrics.internal.example.com:18443:127.0.0.1 'https://metrics.internal.example.com:18443/api/v1/query?query=up'",
			},
		},
	},
}

func examplesUseColor(w io.Writer, mode string) (bool, error) {
	switch mode {
	case "always":
		return true, nil
	case "never":
		return false, nil
	case "auto":
		_, noColor := os.LookupEnv("NO_COLOR")
		if noColor || os.Getenv("TERM") == "dumb" {
			return false, nil
		}
		file, ok := w.(*os.File)
		if !ok {
			return false, nil
		}
		return term.IsTerminal(int(file.Fd())), nil
	default:
		return false, fmt.Errorf("examples: invalid --color %q, expected auto, always, or never", mode)
	}
}

func writeExamples(w io.Writer, color bool) error {
	write := func(format string, args ...any) error {
		_, err := fmt.Fprintf(w, format, args...)
		return err
	}
	if err := write("%s\n", paintExample("KUBE-PORT-FORWARD EXAMPLES", "1;36", color)); err != nil {
		return err
	}
	if err := write("SHELL marks commands; FILE marks input to create. Examples are alternatives, not one script.\n"); err != nil {
		return err
	}
	for _, section := range examplesCatalog {
		if err := write("\n%s\n  %s\n", paintExample("## "+strings.ToUpper(section.title), "1;36", color), section.description); err != nil {
			return err
		}
		for _, example := range section.examples {
			if err := write("\n  %s\n", paintExample("› "+example.title, "1;33", color)); err != nil {
				return err
			}
			if example.description != "" {
				if err := write("    %s\n", example.description); err != nil {
					return err
				}
			}
			for _, file := range example.files {
				if err := write("\n    %s\n", paintExample("FILE · "+file.name+" ("+file.format+")", "1;34", color)); err != nil {
					return err
				}
				for _, line := range strings.Split(file.content, "\n") {
					if err := write("      %s\n", paintExample(line, "34", color)); err != nil {
						return err
					}
				}
			}
			if example.shell != "" {
				if err := write("\n    %s\n", paintExample("SHELL", "1;32", color)); err != nil {
					return err
				}
				for _, line := range strings.Split(example.shell, "\n") {
					if err := write("      %s\n", paintExample(line, "32", color)); err != nil {
						return err
					}
				}
			}
		}
	}
	return write("\nRun 'kube-port-forward --help' for all flags. Full guide: README.md\n")
}

func paintExample(value, style string, color bool) string {
	if !color {
		return value
	}
	return "\x1b[" + style + "m" + value + "\x1b[0m"
}
