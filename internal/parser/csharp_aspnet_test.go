package parser

import (
	"context"
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/pkg/types"
)

func TestParseCSharp_AspNetControllerAndMinimalAPIs(t *testing.T) {
	src := []byte(`
using Microsoft.AspNetCore.Mvc;
using Microsoft.AspNetCore.Builder;

public class UserService {
  public User Find(int id) { return null; }
  public User Save(User u) { return u; }
}

public interface IClock {
  long Now();
}

[ApiController]
[Route("api/[controller]")]
public class UsersController : ControllerBase {
  private readonly UserService _users;

  public UsersController(UserService users) {
    _users = users;
  }

  [HttpGet("{id}")]
  public User Get(int id, [FromServices] IClock clock) {
    _ = clock.Now();
    return _users.Find(id);
  }

  [HttpPost]
  public User Create([FromBody] User body) {
    return _users.Save(body);
  }
}

public class Program {
  public static void Main(string[] args) {
    var builder = WebApplication.CreateBuilder(args);
    builder.Services.AddScoped<UserService>();
    var app = builder.Build();
    app.MapGet("/health", () => "ok");
    app.MapPost("/echo", (EchoReq req, UserService users) => users.Save(req.User));
    app.Run();
  }
}

public class User {}
public class EchoReq { public User User; }
`)
	res, err := ParseCSharp(context.Background(), "c", "Controllers/UsersController.cs", src)
	if err != nil {
		t.Fatal(err)
	}

	syms := map[string]types.Symbol{}
	var ctrl, getMeth, svc *types.Symbol
	var mapGet, mapPost bool
	for i := range res.Symbols {
		s := &res.Symbols[i]
		syms[s.Name] = *s
		switch {
		case s.Name == "UsersController" && s.Kind == types.SymbolKindClass:
			ctrl = s
		case s.Name == "Get" && s.Kind == types.SymbolKindMethod:
			getMeth = s
		case s.Name == "UserService" && s.Kind == types.SymbolKindClass:
			svc = s
		case strings.HasPrefix(s.Name, "aspnet_MapGet_"):
			mapGet = true
			if !strings.Contains(s.Signature, "role=entrypoint") {
				t.Errorf("MapGet site signature=%q want role=entrypoint", s.Signature)
			}
			if !strings.Contains(s.Signature, "frameworks=aspnetcore") {
				t.Errorf("MapGet site missing aspnetcore: %q", s.Signature)
			}
		case strings.HasPrefix(s.Name, "aspnet_MapPost_"):
			mapPost = true
		}
	}
	if ctrl == nil {
		t.Fatalf("missing UsersController; symbols=%v", symbolNames(res))
	}
	if !strings.Contains(ctrl.Signature, "frameworks=aspnetcore") {
		t.Errorf("UsersController signature missing aspnetcore: %q", ctrl.Signature)
	}
	if !strings.Contains(ctrl.Signature, "role=controller") {
		t.Errorf("UsersController missing controller role: %q", ctrl.Signature)
	}
	if svc == nil || !strings.Contains(svc.Signature, "role=service") {
		t.Errorf("UserService missing service role: %#v", svc)
	}
	if getMeth == nil || getMeth.ParentID != "UsersController" {
		t.Errorf("Get ParentID=%v want UsersController", getMeth)
	}
	if getMeth != nil && !strings.Contains(getMeth.Signature, "role=entrypoint") {
		t.Errorf("Get missing entrypoint role: %q", getMeth.Signature)
	}
	if !mapGet || !mapPost {
		t.Errorf("expected aspnet_MapGet_/MapPost_ symbols; got %v", symbolNames(res))
	}

	callsFrom := map[string][]string{}
	for _, e := range res.Edges {
		if e.Kind != types.RefKindCalls {
			continue
		}
		callsFrom[e.SourceID] = append(callsFrom[e.SourceID], symrefName(e.TargetID))
	}
	ctrlCalls := strings.Join(callsFrom[ctrl.ID], ",")
	for _, want := range []string{"UserService", "IClock"} {
		if !strings.Contains(ctrlCalls, want) {
			t.Errorf("UsersController calls missing %q in %v", want, callsFrom[ctrl.ID])
		}
	}
	if getMeth != nil {
		getCalls := strings.Join(callsFrom[getMeth.ID], ",")
		if !strings.Contains(getCalls, "IClock") {
			t.Errorf("Get [FromServices] missing IClock call; got %v", callsFrom[getMeth.ID])
		}
		if !strings.Contains(getCalls, "UserService.Find") && !strings.Contains(getCalls, "Find") {
			t.Errorf("Get missing Find/_users.Find; got %v", callsFrom[getMeth.ID])
		}
	}

	var sawMapPostUsers bool
	var sawAddScoped bool
	for _, e := range res.Edges {
		if e.Kind != types.RefKindCalls {
			continue
		}
		tgt := symrefName(e.TargetID)
		srcName := ""
		for _, s := range res.Symbols {
			if s.ID == e.SourceID {
				srcName = s.Name
				break
			}
		}
		if strings.HasPrefix(srcName, "aspnet_MapPost_") && tgt == "UserService" {
			sawMapPostUsers = true
		}
		if srcName == "Main" && tgt == "UserService" {
			sawAddScoped = true
		}
	}
	if !sawMapPostUsers {
		t.Error("expected MapPost lambda DI edge to UserService")
	}
	if !sawAddScoped {
		t.Error("expected Main→UserService from AddScoped<>")
	}

	got := DetectFrameworkPacks("Controllers/UsersController.cs", nil, string(src))
	found := false
	for _, g := range got {
		if g == "aspnetcore" {
			found = true
		}
	}
	if !found {
		t.Fatalf("DetectFrameworkPacks missing aspnetcore, got %v", got)
	}
}

func symbolNames(res *ParseResult) []string {
	var out []string
	for _, s := range res.Symbols {
		out = append(out, s.Name)
	}
	return out
}

func TestDetectFrameworkPacks_AspNetCore(t *testing.T) {
	t.Parallel()
	got := DetectFrameworkPacks("Program.cs", nil, "var app = WebApplication.CreateBuilder(args).Build();\napp.MapGet(\"/\", () => \"ok\");\n")
	found := false
	for _, g := range got {
		if g == "aspnetcore" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected aspnetcore for Minimal API Program.cs, got %v", got)
	}
	// PHP Controllers must not be tagged.
	got = DetectFrameworkPacks("app/Http/Controllers/UserController.php", nil, "<?php class UserController {}")
	for _, g := range got {
		if g == "aspnetcore" {
			t.Fatalf("PHP controller must not be aspnetcore, got %v", got)
		}
	}
}
