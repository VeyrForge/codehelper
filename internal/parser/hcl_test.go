package parser

import (
	"context"
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/pkg/types"
)

func TestParseHCL_TerraformAddressesAndRefs(t *testing.T) {
	src := []byte(`
variable "region" {
  type    = string
  default = "us-east-1"
}

variable "tags" {
  type = map(string)
}

locals {
  name = lookup(var.tags, "Name", "app-${var.region}")
}

module "vpc" {
  source = "./modules/vpc"
  region = var.region
}

data "aws_ami" "ubuntu" {
  most_recent = true
  owners      = ["099720109477"]
}

resource "aws_instance" "web" {
  ami           = data.aws_ami.ubuntu.id
  instance_type = "t3.micro"
  subnet_id     = module.vpc.public_subnet_id
  tags = {
    Name = local.name
  }
}

output "instance_id" {
  value = aws_instance.web.id
}

provider "aws" {
  region = var.region
}
`)
	res, err := ParseHCL(context.Background(), "repo", "main.tf", src)
	if err != nil {
		t.Fatal(err)
	}
	wantSyms := map[string]bool{
		"var.region":          false,
		"var.tags":            false,
		"local.name":          false,
		"module.vpc":          false,
		"data.aws_ami.ubuntu": false,
		"aws_instance.web":    false,
		"output.instance_id":  false,
		"provider.aws":        false,
	}
	byName := map[string]types.Symbol{}
	for _, s := range res.Symbols {
		byName[s.Name] = s
		if _, ok := wantSyms[s.Name]; ok {
			wantSyms[s.Name] = true
		}
	}
	for name, found := range wantSyms {
		if !found {
			t.Errorf("missing symbol %q; have %+v", name, hclSymNames(byName))
		}
	}
	if s, ok := byName["aws_instance.web"]; !ok || s.Kind != types.SymbolKindClass || s.Signature != "resource" {
		t.Errorf("aws_instance.web kind/sig = %+v", s)
	}
	if s, ok := byName["var.region"]; !ok || s.Kind != types.SymbolKindVariable {
		t.Errorf("var.region kind = %+v", s)
	}

	web := byName["aws_instance.web"]
	var reads []string
	var calls []string
	imports := 0
	for _, e := range res.Edges {
		switch e.Kind {
		case types.RefKindReads:
			if e.SourceID == web.ID {
				reads = append(reads, e.TargetID)
			}
		case types.RefKindCalls:
			calls = append(calls, e.TargetID)
		case types.RefKindImports:
			imports++
		}
	}
	joinedReads := strings.Join(reads, ",")
	for _, want := range []string{"data.aws_ami.ubuntu", "module.vpc", "local.name"} {
		if !strings.Contains(joinedReads, want) {
			t.Errorf("aws_instance.web missing read %q in %v", want, reads)
		}
	}
	outSym := byName["output.instance_id"]
	var outReads bool
	for _, e := range res.Edges {
		if e.Kind == types.RefKindReads && e.SourceID == outSym.ID && strings.Contains(e.TargetID, "aws_instance.web") {
			outReads = true
		}
	}
	if !outReads {
		t.Errorf("output.instance_id should read aws_instance.web; edges=%+v", res.Edges)
	}
	localSym := byName["local.name"]
	var localCallsLookup, localReadsVar bool
	for _, e := range res.Edges {
		if e.SourceID != localSym.ID {
			continue
		}
		if e.Kind == types.RefKindCalls && strings.HasSuffix(e.TargetID, ":lookup") {
			localCallsLookup = true
		}
		if e.Kind == types.RefKindReads && (strings.Contains(e.TargetID, "var.tags") || strings.Contains(e.TargetID, "var.region")) {
			localReadsVar = true
		}
	}
	if !localCallsLookup {
		t.Errorf("local.name should call lookup; calls=%v edges from local=%v", calls, hclEdgeTargets(res, localSym.ID))
	}
	if !localReadsVar {
		t.Errorf("local.name should read var.*; edges=%v", hclEdgeTargets(res, localSym.ID))
	}
	if imports < 1 {
		t.Fatalf("expected module source import, got %d; imports=%v", imports, res.Imports)
	}
	if len(res.Imports) == 0 || res.Imports[0] != "./modules/vpc" {
		t.Errorf("imports = %v want ./modules/vpc", res.Imports)
	}
}

func TestParseHCL_NestedFilterAttributedToData(t *testing.T) {
	src := []byte(`
data "aws_ami" "ubuntu" {
  filter {
    name   = "name"
    values = [var.ami_name]
  }
}
variable "ami_name" {
  type = string
}
`)
	res, err := ParseHCL(context.Background(), "repo", "ami.tf", src)
	if err != nil {
		t.Fatal(err)
	}
	var dataID string
	for _, s := range res.Symbols {
		if s.Name == "data.aws_ami.ubuntu" {
			dataID = s.ID
		}
	}
	if dataID == "" {
		t.Fatalf("missing data symbol; %+v", res.Symbols)
	}
	saw := false
	for _, e := range res.Edges {
		if e.Kind == types.RefKindReads && e.SourceID == dataID && strings.Contains(e.TargetID, "var.ami_name") {
			saw = true
		}
	}
	if !saw {
		t.Fatalf("nested filter should attribute var.ami_name read to data block; edges=%+v", res.Edges)
	}
}

func TestParseHCL_SkipsRequiredProvidersKeys(t *testing.T) {
	src := []byte(`
terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = ">= 5.0"
    }
  }
}

resource "aws_s3_bucket" "this" {
  bucket = var.name
}

variable "name" {
  type = string
}
`)
	res, err := ParseHCL(context.Background(), "repo", "versions.tf", src)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range res.Symbols {
		if s.Name == "aws" {
			t.Fatalf("required_providers key must not become a symbol; got %+v", res.Symbols)
		}
	}
	saw := false
	for _, s := range res.Symbols {
		if s.Name == "aws_s3_bucket.this" {
			saw = true
		}
	}
	if !saw {
		t.Fatalf("expected aws_s3_bucket.this; symbols=%+v", res.Symbols)
	}
}

func hclSymNames(m map[string]types.Symbol) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func hclEdgeTargets(res *ParseResult, from string) []string {
	var out []string
	for _, e := range res.Edges {
		if e.SourceID == from {
			out = append(out, string(e.Kind)+":"+e.TargetID)
		}
	}
	return out
}
