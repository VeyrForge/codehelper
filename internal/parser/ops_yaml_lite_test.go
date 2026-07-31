package parser

import (
	"context"
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/pkg/types"
)

func TestOpsYAMLKind(t *testing.T) {
	cases := map[string]string{
		"k8s/deployment.yaml":          "kubernetes",
		"manifests/api-service.yml":    "kubernetes",
		"kubernetes/ingress.yaml":      "kubernetes",
		"deploy/api-deployment.yaml":   "kubernetes",
		"playbooks/site.yml":           "ansible",
		"roles/web/tasks/main.yml":     "ansible",
		"roles/web/handlers/main.yaml": "ansible",
		"site.yml":                     "ansible",
		"docker-compose.yml":           "", // compose wins via devops
		"openapi.yaml":                 "",
		"random.yml":                   "",
		"main.go":                      "",
	}
	for path, want := range cases {
		if got := opsYAMLKind(path); got != want {
			t.Errorf("%s: got %q want %q", path, got, want)
		}
	}
}

func TestParseKubernetesLite_DeployServiceIngress(t *testing.T) {
	src := []byte(`
apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
spec:
  replicas: 1
---
apiVersion: v1
kind: Service
metadata:
  name: api
spec:
  selector:
    app: api
---
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: api-ingress
spec:
  rules:
  - http:
      paths:
      - path: /
        backend:
          service:
            name: api
`)
	res, err := parseKubernetesLite(context.Background(), "repo", "k8s/all.yaml", src)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]string{}
	for _, s := range res.Symbols {
		found[s.Name] = s.ID
		if s.Language != "kubernetes" {
			t.Errorf("lang %q", s.Language)
		}
	}
	for _, want := range []string{"api", "api-ingress"} {
		if found[want] == "" {
			t.Fatalf("missing %q; got %v", want, found)
		}
	}
	// Deployment + Service share name "api" — last wins in map; both should exist as symbols.
	apiCount := 0
	for _, s := range res.Symbols {
		if s.Name == "api" {
			apiCount++
		}
	}
	if apiCount < 2 {
		t.Fatalf("want Deployment+Service named api, got %d", apiCount)
	}
	saw := false
	for _, e := range res.Edges {
		if e.Kind != types.RefKindReads {
			continue
		}
		if e.SourceID == found["api-ingress"] && (strings.Contains(e.TargetID, ":api") || e.TargetID == found["api"]) {
			saw = true
		}
	}
	if !saw {
		t.Fatal("expected api-ingress READS api service")
	}
}

func TestParseAnsibleLite_PlaybookRoleTasks(t *testing.T) {
	play := []byte(`
- name: Configure web
  hosts: webservers
  roles:
    - web
  tasks:
    - name: Ensure nginx running
      ansible.builtin.service:
        name: nginx
        state: started
`)
	res, err := parseAnsibleLite(context.Background(), "repo", "playbooks/site.yml", play)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]string{}
	for _, s := range res.Symbols {
		found[s.Name] = s.ID
	}
	for _, want := range []string{"Configure web", "Ensure nginx running", "web"} {
		if found[want] == "" {
			t.Errorf("missing %q; got %v", want, found)
		}
	}
	readsWeb := false
	for _, e := range res.Edges {
		if e.Kind == types.RefKindReads && (e.TargetID == found["web"] || strings.HasSuffix(e.TargetID, ":web")) {
			readsWeb = true
		}
	}
	if !readsWeb {
		t.Fatal("expected play/task READS role web")
	}

	roleTasks := []byte(`
- name: Install packages
  ansible.builtin.apt:
    name: nginx
- name: Notify restart
  ansible.builtin.debug:
    msg: done
`)
	rt, err := parseAnsibleLite(context.Background(), "repo", "roles/web/tasks/main.yml", roleTasks)
	if err != nil {
		t.Fatal(err)
	}
	rfound := map[string]bool{}
	for _, s := range rt.Symbols {
		rfound[s.Name] = true
	}
	if !rfound["web"] || !rfound["Install packages"] || !rfound["Notify restart"] {
		t.Fatalf("role tasks: %v", rfound)
	}
}

func TestExtract_RoutesOpsYAMLAndPowerShell(t *testing.T) {
	k8s, err := Extract(context.Background(), "r", "k8s/deployment.yaml", []byte("kind: Deployment\nmetadata:\n  name: web\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(k8s.Symbols) != 1 || k8s.Symbols[0].Name != "web" {
		t.Fatalf("k8s extract: %+v", k8s.Symbols)
	}
	ans, err := Extract(context.Background(), "r", "playbooks/site.yml", []byte("- name: Boot\n  hosts: all\n  roles:\n    - app\n"))
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, s := range ans.Symbols {
		names[s.Name] = true
	}
	if !names["Boot"] || !names["app"] {
		t.Fatalf("ansible extract: %v", names)
	}
	ps, err := Extract(context.Background(), "r", "deploy.ps1", []byte("function Write-Info {\n  Deploy-App\n}\nfunction Deploy-App {\n}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(ps.Symbols) < 2 {
		t.Fatalf("powershell extract: %+v", ps.Symbols)
	}
}
