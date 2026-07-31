package parser

import (
	"context"
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/pkg/types"
)

func TestParseJavaSpringDIAndTypedCalls(t *testing.T) {
	src := []byte(`
package demo;
import org.springframework.stereotype.Service;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.web.bind.annotation.RestController;
import org.springframework.web.bind.annotation.GetMapping;

@Service
class PetService {
  void save(Owner o) {}
}

@RestController
class OwnerController extends BaseController implements OwnerApi {
  private final PetService pets;
  @Autowired
  private OwnerRepository owners;

  public OwnerController(PetService pets) {
    this.pets = pets;
  }

  @GetMapping("/owners")
  public Owner find(int id) {
    return this.owners.findById(id);
  }
}

interface OwnerRepository {
  Owner findById(int id);
}

interface OwnerApi {}
class BaseController {}
class Owner {}
`)
	res, err := ParseJava(context.Background(), "j", "OwnerController.java", src)
	if err != nil {
		t.Fatal(err)
	}
	syms := map[string]types.Symbol{}
	for _, s := range res.Symbols {
		syms[s.Name] = s
	}
	var classCtrl, findMeth *types.Symbol
	for i := range res.Symbols {
		s := &res.Symbols[i]
		switch {
		case s.Name == "OwnerController" && s.Kind == types.SymbolKindClass:
			classCtrl = s
		case s.Name == "find":
			findMeth = s
		}
	}
	if classCtrl == nil {
		t.Fatalf("missing OwnerController class; symbols=%v", keys(syms))
	}
	if !strings.Contains(classCtrl.Signature, "frameworks=spring") {
		t.Errorf("OwnerController signature missing spring: %q", classCtrl.Signature)
	}
	if !strings.Contains(classCtrl.Signature, "role=controller") {
		t.Errorf("OwnerController missing controller role: %q", classCtrl.Signature)
	}
	if !strings.Contains(syms["PetService"].Signature, "role=service") {
		t.Errorf("PetService missing service role: %q", syms["PetService"].Signature)
	}
	if findMeth == nil || findMeth.ParentID != "OwnerController" {
		t.Errorf("find ParentID=%v want OwnerController", findMeth)
	}

	var calls []string
	var inherits, implements int
	for _, e := range res.Edges {
		switch e.Kind {
		case types.RefKindCalls:
			if i := strings.LastIndex(e.TargetID, ":"); i >= 0 {
				calls = append(calls, e.TargetID[i+1:])
			}
		case types.RefKindInherits:
			inherits++
		case types.RefKindImplements:
			implements++
		}
	}
	joined := strings.Join(calls, ",")
	for _, want := range []string{"PetService", "OwnerRepository", "OwnerRepository.findById"} {
		if !strings.Contains(joined, want) {
			t.Errorf("expected call target %q in %v", want, calls)
		}
	}
	if inherits == 0 {
		t.Error("expected inherits edge to BaseController")
	}
	if implements == 0 {
		t.Error("expected implements edge to OwnerApi")
	}
}

func TestParseKotlinSpringDI(t *testing.T) {
	src := []byte(`
package demo

import org.springframework.stereotype.Service
import org.springframework.web.bind.annotation.RestController
import org.springframework.web.bind.annotation.GetMapping

@Service
class PetService {
    fun save(o: Owner) {}
}

@RestController
class OwnerController(
    private val pets: PetService
) : BaseController(), OwnerApi {
    @GetMapping("/owners")
    fun find(id: Int): Owner {
        pets.save(Owner())
        return Owner()
    }
}

interface OwnerApi
open class BaseController
class Owner
`)
	res, err := ParseKotlin(context.Background(), "k", "OwnerController.kt", src)
	if err != nil {
		t.Fatal(err)
	}
	syms := map[string]types.Symbol{}
	for _, s := range res.Symbols {
		syms[s.Name] = s
	}
	if _, ok := syms["OwnerController"]; !ok {
		t.Fatalf("missing OwnerController; got %v", keys(syms))
	}
	if !strings.Contains(syms["OwnerController"].Signature, "frameworks=spring") {
		t.Errorf("OwnerController signature=%q", syms["OwnerController"].Signature)
	}
	if syms["find"].ParentID != "OwnerController" {
		t.Errorf("find ParentID=%q", syms["find"].ParentID)
	}
	var calls []string
	var imports int
	for _, e := range res.Edges {
		switch e.Kind {
		case types.RefKindCalls:
			if i := strings.LastIndex(e.TargetID, ":"); i >= 0 {
				calls = append(calls, e.TargetID[i+1:])
			}
		case types.RefKindImports:
			imports++
		}
	}
	joined := strings.Join(calls, ",")
	if !strings.Contains(joined, "PetService") {
		t.Errorf("expected DI call to PetService in %v", calls)
	}
	if !strings.Contains(joined, "PetService.save") && !strings.Contains(joined, "save") {
		t.Errorf("expected save call in %v", calls)
	}
	if imports == 0 {
		t.Error("expected kotlin import edges")
	}
}

func TestDetectFrameworkPacks_Spring(t *testing.T) {
	t.Parallel()
	got := DetectFrameworkPacks("src/OwnerController.java", nil, `
package demo;
import org.springframework.web.bind.annotation.RestController;
@RestController
class OwnerController {}
`)
	found := false
	for _, g := range got {
		if g == "spring" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected spring, got %v", got)
	}
	// Nest must not be tagged spring via bare @Controller on .ts
	got = DetectFrameworkPacks("cats.controller.ts", nil, `import { Controller } from "@nestjs/common";
@Controller("cats")
export class CatsController {}
`)
	for _, g := range got {
		if g == "spring" {
			t.Fatalf("Nest controller must not be spring, got %v", got)
		}
	}
}

func keys(m map[string]types.Symbol) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
