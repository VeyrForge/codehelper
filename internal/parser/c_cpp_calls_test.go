package parser

import (
	"context"
	"strings"
	"testing"

	"github.com/VeyrForge/codehelper/pkg/types"
)

func TestParseCpp_CallEdges(t *testing.T) {
	t.Parallel()
	src := []byte(`
#include "helper.h"
void helper();
int run(int x) {
  helper();
  return x + 1;
}
`)
	res, err := ParseCpp(context.Background(), "r", "run.cpp", src)
	if err != nil {
		t.Fatal(err)
	}
	var sawHelper bool
	for _, e := range res.Edges {
		if e.Kind == types.RefKindCalls && strings.HasSuffix(e.TargetID, ":helper") {
			sawHelper = true
		}
	}
	if !sawHelper {
		t.Fatalf("expected calls edge to helper; edges=%+v", res.Edges)
	}
}

func TestParseCpp_MethodParentAndThisCalls(t *testing.T) {
	t.Parallel()
	src := []byte(`
class Engine {
public:
  void tick();
  void start() {
    tick();
    this->tick();
  }
};

void Engine::tick() {
  helper();
}
`)
	res, err := ParseCpp(context.Background(), "r", "engine.cpp", src)
	if err != nil {
		t.Fatal(err)
	}
	var startSym, tickInline *types.Symbol
	for i := range res.Symbols {
		s := &res.Symbols[i]
		switch {
		case s.Name == "start" && s.Kind == types.SymbolKindMethod:
			startSym = s
		case s.Name == "tick" && s.Kind == types.SymbolKindMethod && s.ParentID == "Engine" && strings.Contains(s.ID, "engine.cpp"):
			// Prefer in-class definition when both exist.
			if tickInline == nil || s.LineStart < tickInline.LineStart {
				tickInline = s
			}
		}
	}
	if startSym == nil || startSym.ParentID != "Engine" {
		t.Fatalf("expected Engine.start method with ParentID; symbols=%+v", res.Symbols)
	}
	var sawTyped, sawBare bool
	for _, e := range res.Edges {
		if e.Kind != types.RefKindCalls || e.SourceID != startSym.ID {
			continue
		}
		if strings.HasSuffix(e.TargetID, ":Engine.tick") {
			sawTyped = true
		}
		if strings.HasSuffix(e.TargetID, ":tick") {
			sawBare = true
		}
	}
	if !sawTyped {
		t.Fatalf("expected typed Engine.tick call from start; edges=%+v", res.Edges)
	}
	// Bare tick() may also appear when not rewritten; typed this->tick is required.
	_ = sawBare
}

func TestParseCpp_HeaderMethodsAndBase(t *testing.T) {
	t.Parallel()
	src := []byte(`
class Widget : public BaseWidget {
public:
  void draw();
  void resize(int w, int h);
private:
  int width_;
};
`)
	res, err := ParseCpp(context.Background(), "r", "widget.h", src)
	if err != nil {
		t.Fatal(err)
	}
	var sawDraw, sawResize, sawWidth, sawInherits bool
	for _, s := range res.Symbols {
		switch {
		case s.Name == "draw" && s.Kind == types.SymbolKindMethod && s.ParentID == "Widget":
			sawDraw = true
		case s.Name == "resize" && s.Kind == types.SymbolKindMethod && s.ParentID == "Widget":
			sawResize = true
		case s.Name == "width_" && s.Kind == types.SymbolKindVariable && s.ParentID == "Widget":
			sawWidth = true
		}
	}
	for _, e := range res.Edges {
		if e.Kind == types.RefKindInherits && strings.HasSuffix(e.TargetID, ":BaseWidget") {
			sawInherits = true
		}
	}
	if !sawDraw || !sawResize || !sawWidth || !sawInherits {
		t.Fatalf("header extract incomplete draw=%v resize=%v width=%v inherits=%v symbols=%+v edges=%+v",
			sawDraw, sawResize, sawWidth, sawInherits, res.Symbols, res.Edges)
	}
	for _, s := range res.Symbols {
		if s.Name == "Widget" && !strings.Contains(s.Signature, "embeds=BaseWidget") {
			t.Fatalf("Widget should embed BaseWidget, got %q", s.Signature)
		}
	}
}

func TestExtract_UnrealDotHUsesCpp(t *testing.T) {
	t.Parallel()
	src := []byte(`
#include "CoreMinimal.h"
#include "MyActor.generated.h"
UCLASS()
class AMyActor : public AActor
{
	GENERATED_BODY()
public:
	virtual void BeginPlay() override;
};
`)
	res, err := Extract(context.Background(), "r", "Source/MyGame/MyActor.h", src)
	if err != nil {
		t.Fatal(err)
	}
	var sawClass, sawBegin bool
	for _, s := range res.Symbols {
		if s.Name == "AMyActor" && s.Kind == types.SymbolKindClass {
			sawClass = true
		}
		if s.Name == "BeginPlay" && s.Kind == types.SymbolKindMethod && s.ParentID == "AMyActor" {
			sawBegin = true
		}
	}
	if !sawClass || !sawBegin {
		t.Fatalf("UE .h must route to ParseCpp; symbols=%+v", res.Symbols)
	}
}

func TestParseCpp_UnrealMacrosAndTemplateReads(t *testing.T) {
	t.Parallel()
	hdr := []byte(`
#include "CoreMinimal.h"
#include "HealthComponent.h"
#include "MyGameCharacter.generated.h"

UCLASS(BlueprintType)
class AMyGameCharacter : public ACharacter
{
	GENERATED_BODY()
public:
	virtual void BeginPlay() override;
	UFUNCTION(BlueprintCallable)
	void TakeDamage(float Amount);
	UPROPERTY()
	UHealthComponent* HealthComp;
};
`)
	cppSrc := []byte(`
#include "MyGameCharacter.h"

void AMyGameCharacter::BeginPlay()
{
	Super::BeginPlay();
	HealthComp = CreateDefaultSubobject<UHealthComponent>(TEXT("HealthComp"));
	if (UHealthComponent* Comp = Cast<UHealthComponent>(HealthComp))
	{
		Comp->ApplyDamage(0.f);
	}
}
`)
	hRes, err := ParseCpp(context.Background(), "r", "MyGameCharacter.h", hdr)
	if err != nil {
		t.Fatal(err)
	}
	cRes, err := ParseCpp(context.Background(), "r", "MyGameCharacter.cpp", cppSrc)
	if err != nil {
		t.Fatal(err)
	}

	var sawBegin, sawTake, sawField bool
	var genBody bool
	for _, s := range hRes.Symbols {
		switch s.Name {
		case "BeginPlay":
			sawBegin = s.Kind == types.SymbolKindMethod && s.ParentID == "AMyGameCharacter"
		case "TakeDamage":
			sawTake = s.Kind == types.SymbolKindMethod && s.ParentID == "AMyGameCharacter"
		case "HealthComp":
			sawField = s.Kind == types.SymbolKindVariable && s.ParentID == "AMyGameCharacter"
		case "GENERATED_BODY", "UFUNCTION", "UPROPERTY", "UCLASS":
			genBody = true
		}
	}
	if !sawBegin || !sawTake || !sawField {
		t.Fatalf("UE header symbols missing begin=%v take=%v field=%v symbols=%+v", sawBegin, sawTake, sawField, hRes.Symbols)
	}
	if genBody {
		t.Fatalf("UE macros must not become symbols; symbols=%+v", hRes.Symbols)
	}
	// Engine base ACharacter must not create inherits hub.
	for _, e := range hRes.Edges {
		if e.Kind == types.RefKindInherits && strings.HasSuffix(e.TargetID, ":ACharacter") {
			t.Fatalf("unexpected inherits to engine base ACharacter: %+v", e)
		}
	}
	var fieldReadsHealth bool
	for _, e := range hRes.Edges {
		if e.Kind == types.RefKindReads && strings.HasSuffix(e.TargetID, ":UHealthComponent") {
			fieldReadsHealth = true
		}
	}
	if !fieldReadsHealth {
		t.Fatalf("expected UHealthComponent field reads; edges=%+v", hRes.Edges)
	}

	var beginSym *types.Symbol
	for i := range cRes.Symbols {
		s := &cRes.Symbols[i]
		if s.Name == "BeginPlay" && s.Kind == types.SymbolKindMethod && s.ParentID == "AMyGameCharacter" {
			beginSym = s
			break
		}
	}
	if beginSym == nil {
		t.Fatalf("BeginPlay definition missing; symbols=%+v", cRes.Symbols)
	}
	var sawTemplateRead, sawApplyCall bool
	for _, e := range cRes.Edges {
		if e.SourceID != beginSym.ID {
			continue
		}
		if e.Kind == types.RefKindReads && strings.HasSuffix(e.TargetID, ":UHealthComponent") {
			sawTemplateRead = true
		}
		if e.Kind == types.RefKindCalls && (strings.HasSuffix(e.TargetID, ":ApplyDamage") || strings.Contains(e.TargetID, ".ApplyDamage")) {
			sawApplyCall = true
		}
	}
	if !sawTemplateRead {
		t.Fatalf("expected Cast/CreateDefaultSubobject reads to UHealthComponent; edges=%+v", cRes.Edges)
	}
	if !sawApplyCall {
		t.Fatalf("expected ApplyDamage call edge from BeginPlay; edges=%+v", cRes.Edges)
	}
	var sawTypedApply bool
	for _, e := range cRes.Edges {
		if e.SourceID != beginSym.ID || e.Kind != types.RefKindCalls {
			continue
		}
		if strings.HasSuffix(e.TargetID, ":UHealthComponent.ApplyDamage") ||
			strings.Contains(e.TargetID, "UHealthComponent.ApplyDamage") {
			sawTypedApply = true
		}
	}
	if !sawTypedApply {
		t.Fatalf("expected typed UHealthComponent.ApplyDamage from Comp/HealthComp; edges=%+v", cRes.Edges)
	}
}

func TestParseC_CallEdges(t *testing.T) {
	t.Parallel()
	src := []byte(`
#include "util.h"
void util(void);
void main_fn(void) {
  util();
}
`)
	res, err := ParseC(context.Background(), "r", "main.c", src)
	if err != nil {
		t.Fatal(err)
	}
	var sawUtil bool
	for _, e := range res.Edges {
		if e.Kind == types.RefKindCalls && strings.HasSuffix(e.TargetID, ":util") {
			sawUtil = true
		}
	}
	if !sawUtil {
		t.Fatalf("expected calls edge to util; edges=%+v", res.Edges)
	}
}
