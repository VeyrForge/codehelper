package parser

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	cpp "github.com/smacker/go-tree-sitter/cpp"

	"github.com/VeyrForge/codehelper/pkg/types"
)

// ParseCpp extracts C++ declarations, method ParentID (enclosing class/struct),
// and call edges. Methods use extractCallsScoped with this→Class and typed
// field/local receivers (HealthComp→UHealthComponent) so context / impact can
// resolve Class.method denser than bare heuristic names.
//
// Unreal-oriented extensions (when UCLASS/GENERATED_BODY/CoreMinimal markers are
// present, and generally for header density): in-class method/field declarations,
// base_class_clause inherits (engine bases skipped), and Cast/
// CreateDefaultSubobject/NewObject/LoadObject template type reads.
func ParseCpp(ctx context.Context, repoID, relPath string, buf []byte) (*ParseResult, error) {
	p := sitter.NewParser()
	p.SetLanguage(cpp.GetLanguage())
	tree, err := p.ParseCtx(ctx, nil, buf)
	if err != nil {
		return nil, err
	}
	out := &ParseResult{}
	fid := FileNodeID(repoID, relPath)
	ue := cppLooksUnreal(buf, relPath)
	var typeStack []string
	fieldsByClass := map[string]map[string]string{}
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n == nil {
			return
		}
		switch n.Type() {
		case "preproc_include":
			path := includeString(n, buf)
			if path == "" {
				return
			}
			out.Imports = append(out.Imports, path)
			out.Edges = append(out.Edges, types.Reference{
				ID:         edgeID(repoID, fid, moduleNodeID(repoID, path), "imports"),
				RepoID:     repoID,
				Kind:       types.RefKindImports,
				SourceID:   fid,
				TargetID:   moduleNodeID(repoID, path),
				Confidence: 0.85,
			})
			return
		case "class_specifier", "struct_specifier":
			name := ChildName(n, "name", buf)
			if name == "" {
				for i := 0; i < int(n.ChildCount()); i++ {
					walk(n.Child(i))
				}
				return
			}
			embeds := cppCollectBaseNames(n, buf)
			sig := appendEmbedsSig("", embeds)
			sym := symbol(repoID, relPath, name, types.SymbolKindClass, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, "cpp", sig, "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			cppEmitBaseClasses(n, buf, repoID, relPath, sym.ID, out)
			fieldsByClass[name] = cppCollectFieldTypes(n, buf)
			typeStack = append(typeStack, name)
			for i := 0; i < int(n.ChildCount()); i++ {
				walk(n.Child(i))
			}
			typeStack = typeStack[:len(typeStack)-1]
			return
		case "namespace_definition":
			name := ChildName(n, "name", buf)
			if name != "" {
				sym := symbol(repoID, relPath, name, types.SymbolKindNamespace, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, "cpp", "", "")
				out.Symbols = append(out.Symbols, sym)
				out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			}
			for i := 0; i < int(n.ChildCount()); i++ {
				walk(n.Child(i))
			}
			return
		case "field_declaration", "declaration":
			if len(typeStack) > 0 {
				cppEmitClassMember(n, buf, repoID, relPath, typeStack[len(typeStack)-1], out, ue)
				return
			}
			// Free-standing: walk children (nested types / rare local classes).
			for i := 0; i < int(n.ChildCount()); i++ {
				walk(n.Child(i))
			}
			return
		case "function_definition":
			decl := n.ChildByFieldName("declarator")
			name, qual := cppFuncName(decl, buf)
			if name == "" || cppSkipCallable(name) {
				return
			}
			parent := ""
			kind := types.SymbolKindFunction
			switch {
			case len(typeStack) > 0:
				parent = typeStack[len(typeStack)-1]
				kind = types.SymbolKindMethod
			case qual != "":
				// Out-of-line Class::method — still a method for ParentID / recv.
				parent = qual
				kind = types.SymbolKindMethod
			}
			// Skip Class::Class / Class::~Class ctors — collide with class name.
			if parent != "" && (name == parent || name == "~"+parent) {
				return
			}
			sym := symbol(repoID, relPath, name, kind, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, "cpp", "", parent)
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			fields := map[string]string{}
			if parent != "" {
				for k, v := range fieldsByClass[parent] {
					fields[k] = v
				}
			}
			cppMergeLocalTypes(n, buf, fields)
			if ue {
				cppMergeUEFactoryTypes(n, buf, fields)
			}
			extractCallsScoped(n, buf, repoID, relPath, sym.ID, out, cppTypeOf(parent, fields))
			if ue {
				cppEmitUETemplateTypes(n, buf, repoID, relPath, sym.ID, out)
				cppEmitTypeReads(n, buf, repoID, relPath, sym.ID, out)
			}
			return
		}
		for i := 0; i < int(n.ChildCount()); i++ {
			walk(n.Child(i))
		}
	}
	walk(tree.RootNode())
	return out, nil
}

// cppTypeOf maps this/*this and typed fields/locals to Class.method edges.
func cppTypeOf(className string, fields map[string]string) func(string) string {
	if className == "" && len(fields) == 0 {
		return nil
	}
	return func(recv string) string {
		recv = strings.TrimSpace(recv)
		switch recv {
		case "this", "*this":
			return className
		}
		if recv == "" {
			return ""
		}
		// Peel this->HealthComp / HealthComp.
		if strings.HasPrefix(recv, "this->") {
			recv = strings.TrimPrefix(recv, "this->")
		} else if strings.HasPrefix(recv, "this.") {
			recv = strings.TrimPrefix(recv, "this.")
		}
		if i := strings.IndexAny(recv, ".>"); i > 0 {
			recv = recv[:i]
		}
		if typ, ok := fields[recv]; ok {
			return typ
		}
		return ""
	}
}

// cppCollectFieldTypes maps member names → UObject/class type leaf for typed calls.
func cppCollectFieldTypes(classNode *sitter.Node, buf []byte) map[string]string {
	out := map[string]string{}
	if classNode == nil {
		return out
	}
	Walk(classNode, func(n *sitter.Node) {
		if n == nil {
			return
		}
		switch n.Type() {
		case "field_declaration", "declaration":
			if cppFieldIsUEMacroStub(n, buf) {
				return
			}
			fname := cppFieldName(n, buf)
			typ := cppFieldTypeName(n, buf)
			if fname == "" || typ == "" || cppSkipType(typ) {
				return
			}
			out[fname] = typ
		}
	})
	return out
}

// cppMergeLocalTypes adds local/parameter Type* name bindings into fields.
func cppMergeLocalTypes(fn *sitter.Node, buf []byte, fields map[string]string) {
	if fn == nil || fields == nil {
		return
	}
	Walk(fn, func(n *sitter.Node) {
		if n == nil {
			return
		}
		switch n.Type() {
		case "declaration", "init_declarator", "parameter_declaration":
		default:
			return
		}
		typ := cppFieldTypeName(n, buf)
		if typ == "" {
			// parameter_declaration: type is often a named child, not "type" field.
			for i := 0; i < int(n.NamedChildCount()); i++ {
				c := n.NamedChild(i)
				if c == nil {
					continue
				}
				if c.Type() == "type_identifier" {
					tok := strings.TrimSpace(c.Content(buf))
					if tok != "" && !cppSkipType(tok) && !cppSkipCallable(tok) {
						typ = tok
						break
					}
				}
			}
		}
		name := cppFieldName(n, buf)
		if name == "" {
			if d := n.ChildByFieldName("declarator"); d != nil {
				name = FirstIdentifier(d, buf)
			}
		}
		if name == "" || typ == "" || cppSkipType(typ) {
			return
		}
		fields[name] = typ
	})
}

// cppMergeUEFactoryTypes maps lhs of CreateDefaultSubobject/NewObject/Cast assigns.
var (
	cppUEFactoryAssignRe = regexp.MustCompile(
		`\b([A-Za-z_]\w*)\s*=\s*(?:CreateDefaultSubobject|NewObject|Cast|CastChecked|LoadObject|CreateWidget)\s*<\s*([A-Z][A-Za-z0-9_]*)\s*>`)
	cppUECastInitRe = regexp.MustCompile(
		`\b([A-Z][A-Za-z0-9_]*)\s*\*\s*([A-Za-z_]\w*)\s*=\s*(?:Cast|CastChecked)\s*<`)
)

func cppMergeUEFactoryTypes(fn *sitter.Node, buf []byte, fields map[string]string) {
	if fn == nil || fields == nil {
		return
	}
	src := fn.Content(buf)
	for _, m := range cppUEFactoryAssignRe.FindAllStringSubmatch(src, -1) {
		name, typ := strings.TrimSpace(m[1]), strings.TrimSpace(m[2])
		if name == "" || typ == "" || cppSkipType(typ) {
			continue
		}
		fields[name] = typ
	}
	// Cast init: UHealthComponent* Comp = Cast<UHealthComponent>(…)
	for _, m := range cppUECastInitRe.FindAllStringSubmatch(src, -1) {
		typ, name := strings.TrimSpace(m[1]), strings.TrimSpace(m[2])
		if name == "" || typ == "" || cppSkipType(typ) {
			continue
		}
		fields[name] = typ
	}
}

func cppSkipCallable(name string) bool {
	// Avoid indexing noisy compiler/operator sugar as graph hubs.
	if strings.HasPrefix(name, "operator") {
		return true
	}
	switch name {
	case "GENERATED_BODY", "GENERATED_UCLASS_BODY", "GENERATED_USTRUCT_BODY",
		"GENERATED_UINTERFACE_BODY", "GENERATED_IINTERFACE_BODY",
		"UCLASS", "USTRUCT", "UENUM", "UINTERFACE", "UFUNCTION", "UPROPERTY",
		"UPARAM", "UMETA", "UDELEGATE", "DECLARE_DYNAMIC_MULTICAST_DELEGATE",
		"TEXT", "LOCTEXT", "NSLOCTEXT":
		return true
	default:
		return false
	}
}

func cppLooksUnreal(buf []byte, relPath string) bool {
	src := string(buf)
	switch {
	case strings.Contains(src, "GENERATED_BODY"),
		strings.Contains(src, "UCLASS("),
		strings.Contains(src, "UFUNCTION("),
		strings.Contains(src, "UPROPERTY("),
		strings.Contains(src, "CoreMinimal.h"),
		strings.Contains(src, ".generated.h"),
		// Out-of-line .cpp often lacks macros but still uses UE factories.
		strings.Contains(src, "CreateDefaultSubobject"),
		strings.Contains(src, "NewObject<"),
		strings.Contains(src, "Cast<"),
		strings.Contains(src, "LoadObject<"),
		strings.Contains(src, "GetMutableDefault"):
		return true
	default:
		_ = relPath
		return false
	}
}

// looksLikeCppHeader reports .h buffers that should use the C++ extractor
// (Unreal UCLASS headers and typical C++ class/namespace headers).
func looksLikeCppHeader(buf []byte, relPath string) bool {
	if cppLooksUnreal(buf, relPath) {
		return true
	}
	src := string(buf)
	if strings.Contains(src, "public:") || strings.Contains(src, "private:") ||
		strings.Contains(src, "protected:") || strings.Contains(src, "namespace ") ||
		strings.Contains(src, "template<") || strings.Contains(src, "template <") {
		return true
	}
	return strings.Contains(src, "class ") && strings.Contains(src, "{")
}

func cppSkipType(tok string) bool {
	switch tok {
	case "int", "float", "double", "bool", "char", "void", "auto", "size_t",
		"int8", "int16", "int32", "int64", "uint8", "uint16", "uint32", "uint64",
		"FString", "FName", "FText", "FVector", "FRotator", "FQuat", "FTransform",
		"FHitResult", "TArray", "TMap", "TSet", "TWeakObjectPtr", "TSoftObjectPtr",
		"TSubclassOf", "TObjectPtr", "TRUE", "FALSE", "NULL",
		// Engine bases / ubiquitous types — skip inherits hubs (Unity pattern).
		"UObject", "AActor", "APawn", "ACharacter", "AController", "APlayerController",
		"AGameModeBase", "AGameMode", "AGameStateBase", "APlayerState",
		"UActorComponent", "USceneComponent", "UPrimitiveComponent", "UStaticMeshComponent",
		"USkeletalMeshComponent", "UCapsuleComponent", "UCameraComponent",
		"UUserWidget", "UWorld", "UClass", "UBlueprint", "UBlueprintGeneratedClass",
		"Super", "ThisClass", "FObjectInitializer":
		return true
	default:
		return false
	}
}

// cppEmitBaseClasses emits inherits for `: public Base` clauses.
// Engine bases (AActor/ACharacter/UObject/…) are skipped to avoid drowning hubs.
func cppEmitBaseClasses(n *sitter.Node, buf []byte, repoID, relPath, fromSym string, out *ParseResult) {
	if n == nil || out == nil {
		return
	}
	for _, tok := range cppCollectBaseNames(n, buf) {
		tgt := fmt.Sprintf("symref:%s:%s:%s", repoID, relPath, tok)
		out.Edges = append(out.Edges, types.Reference{
			ID:         edgeID(repoID, fromSym, tgt, "inherits"),
			RepoID:     repoID,
			Kind:       types.RefKindInherits,
			SourceID:   fromSym,
			TargetID:   tgt,
			Confidence: 0.9,
		})
	}
}

// cppCollectBaseNames returns Capitalized non-engine base class leaf names for
// embeds= densify and inherits edges.
func cppCollectBaseNames(n *sitter.Node, buf []byte) []string {
	if n == nil {
		return nil
	}
	var clause *sitter.Node
	for i := 0; i < int(n.ChildCount()); i++ {
		c := n.Child(i)
		if c != nil && c.Type() == "base_class_clause" {
			clause = c
			break
		}
	}
	if clause == nil {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for i := 0; i < int(clause.NamedChildCount()); i++ {
		ch := clause.NamedChild(i)
		if ch == nil {
			continue
		}
		tok := ""
		switch ch.Type() {
		case "type_identifier", "identifier":
			tok = strings.TrimSpace(ch.Content(buf))
		case "qualified_identifier", "template_type":
			tok = strings.TrimSpace(ch.Content(buf))
			if j := strings.LastIndex(tok, "::"); j >= 0 {
				tok = tok[j+2:]
			}
			if j := strings.IndexAny(tok, "<"); j > 0 {
				tok = tok[:j]
			}
		default:
			continue
		}
		tok = strings.TrimSpace(tok)
		if tok == "" || seen[tok] || cppSkipType(tok) {
			continue
		}
		if tok[0] < 'A' || tok[0] > 'Z' {
			continue
		}
		seen[tok] = true
		out = append(out, tok)
	}
	return out
}

// cppEmitClassMember extracts in-class method declarations and fields
// (header-heavy Unreal / C++ density). Skips misparsed UFUNCTION/UPROPERTY macros.
func cppEmitClassMember(n *sitter.Node, buf []byte, repoID, relPath, parent string, out *ParseResult, ue bool) {
	if n == nil || out == nil || parent == "" {
		return
	}
	if cppFieldIsUEMacroStub(n, buf) {
		return
	}
	if fd := cppFindFunctionDeclarator(n); fd != nil {
		name, _ := cppFuncName(fd, buf)
		if name == "" {
			name = FirstIdentifier(fd, buf)
		}
		if name == "" || cppSkipCallable(name) {
			return
		}
		// Skip ctor/dtor decls (same name as class) — they collide with the
		// class symbol and poison Cast/CreateDefaultSubobject type-read resolve.
		if name == parent || name == "~"+parent {
			return
		}
		sym := symbol(repoID, relPath, name, types.SymbolKindMethod, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, "cpp", "", parent)
		out.Symbols = append(out.Symbols, sym)
		out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
		return
	}
	// Field / UPROPERTY-backed member.
	fname := cppFieldName(n, buf)
	if fname == "" || cppSkipCallable(fname) {
		return
	}
	sym := symbol(repoID, relPath, fname, types.SymbolKindVariable, int(n.StartPoint().Row)+1, int(n.EndPoint().Row)+1, "cpp", "", parent)
	out.Symbols = append(out.Symbols, sym)
	out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
	if typeTok := cppFieldTypeName(n, buf); typeTok != "" && !cppSkipType(typeTok) {
		tgt := fmt.Sprintf("symref:%s:%s:%s", repoID, relPath, typeTok)
		out.Edges = append(out.Edges, types.Reference{
			ID:         edgeID(repoID, sym.ID, tgt, "reads"),
			RepoID:     repoID,
			Kind:       types.RefKindReads,
			SourceID:   sym.ID,
			TargetID:   tgt,
			Confidence: 0.75,
		})
		// Also attach type read on the enclosing class for agent locate fan-in.
		clsID := ""
		for i := range out.Symbols {
			s := &out.Symbols[i]
			if s.Name == parent && s.Kind == types.SymbolKindClass {
				clsID = s.ID
				break
			}
		}
		if clsID != "" {
			out.Edges = append(out.Edges, types.Reference{
				ID:         edgeID(repoID, clsID, tgt, "reads"),
				RepoID:     repoID,
				Kind:       types.RefKindReads,
				SourceID:   clsID,
				TargetID:   tgt,
				Confidence: 0.7,
			})
		}
	}
	_ = ue
}

func cppFieldIsUEMacroStub(n *sitter.Node, buf []byte) bool {
	for i := 0; i < int(n.NamedChildCount()); i++ {
		c := n.NamedChild(i)
		if c == nil {
			continue
		}
		if c.Type() == "type_identifier" || c.Type() == "identifier" {
			tok := strings.TrimSpace(c.Content(buf))
			if cppSkipCallable(tok) {
				return true
			}
		}
	}
	return false
}

func cppFindFunctionDeclarator(n *sitter.Node) *sitter.Node {
	if n == nil {
		return nil
	}
	if n.Type() == "function_declarator" {
		return n
	}
	if d := n.ChildByFieldName("declarator"); d != nil {
		switch d.Type() {
		case "function_declarator":
			return d
		case "pointer_declarator", "reference_declarator", "array_declarator":
			return cppFindFunctionDeclarator(d)
		}
	}
	for i := 0; i < int(n.NamedChildCount()); i++ {
		c := n.NamedChild(i)
		if c == nil {
			continue
		}
		switch c.Type() {
		case "function_declarator":
			return c
		case "pointer_declarator", "reference_declarator":
			if fd := cppFindFunctionDeclarator(c); fd != nil {
				return fd
			}
		}
	}
	return nil
}

func cppFieldName(n *sitter.Node, buf []byte) string {
	if n == nil {
		return ""
	}
	var walk func(node *sitter.Node) string
	walk = func(node *sitter.Node) string {
		if node == nil {
			return ""
		}
		switch node.Type() {
		case "field_identifier", "identifier":
			return strings.TrimSpace(node.Content(buf))
		case "pointer_declarator", "reference_declarator", "array_declarator", "init_declarator":
			for i := 0; i < int(node.NamedChildCount()); i++ {
				if s := walk(node.NamedChild(i)); s != "" {
					return s
				}
			}
		}
		return ""
	}
	if d := n.ChildByFieldName("declarator"); d != nil {
		if s := walk(d); s != "" {
			return s
		}
	}
	for i := 0; i < int(n.NamedChildCount()); i++ {
		c := n.NamedChild(i)
		if c == nil {
			continue
		}
		switch c.Type() {
		case "field_identifier":
			return strings.TrimSpace(c.Content(buf))
		case "pointer_declarator", "reference_declarator", "init_declarator":
			if s := walk(c); s != "" {
				return s
			}
		}
	}
	return ""
}

func cppFieldTypeName(n *sitter.Node, buf []byte) string {
	if n == nil {
		return ""
	}
	for i := 0; i < int(n.NamedChildCount()); i++ {
		c := n.NamedChild(i)
		if c == nil {
			continue
		}
		switch c.Type() {
		case "type_identifier":
			tok := strings.TrimSpace(c.Content(buf))
			if tok != "" && !cppSkipCallable(tok) {
				return tok
			}
		case "qualified_identifier", "template_type":
			tok := strings.TrimSpace(c.Content(buf))
			if j := strings.LastIndex(tok, "::"); j >= 0 {
				tok = tok[j+2:]
			}
			if j := strings.IndexAny(tok, "<"); j > 0 {
				tok = tok[:j]
			}
			tok = strings.TrimSpace(tok)
			if tok != "" && !cppSkipCallable(tok) && !cppSkipType(tok) {
				return tok
			}
		}
	}
	return ""
}

// Unreal / engine template factories that name a UObject type in <T>.
var cppUETemplateTypeRe = regexp.MustCompile(
	`\b(?:Cast|CastChecked|NewObject|CreateDefaultSubobject|CreateWidget|LoadObject|LoadClass|StaticLoadObject|StaticLoadClass|GetMutableDefault|GetDefault|DuplicateObject|ConstructorHelpers::FObjectFinder|ConstructorHelpers::FClassFinder)\s*<\s*([A-Z][A-Za-z0-9_]*)\s*>`)

func cppEmitUETemplateTypes(root *sitter.Node, buf []byte, repoID, relPath, fromSym string, out *ParseResult) {
	if root == nil || out == nil {
		return
	}
	src := root.Content(buf)
	seen := map[string]bool{}
	for _, m := range cppUETemplateTypeRe.FindAllStringSubmatch(src, -1) {
		tok := strings.TrimSpace(m[1])
		if tok == "" || cppSkipType(tok) || seen[tok] {
			continue
		}
		seen[tok] = true
		tgt := fmt.Sprintf("symref:%s:%s:%s", repoID, relPath, tok)
		out.Edges = append(out.Edges, types.Reference{
			ID:         edgeID(repoID, fromSym, tgt, "reads"),
			RepoID:     repoID,
			Kind:       types.RefKindReads,
			SourceID:   fromSym,
			TargetID:   tgt,
			Confidence: 0.85,
		})
	}
}

// cppEmitTypeReads emits reads for capitalized type identifiers in a method body
// (Unreal scripts referencing other UObject types).
func cppEmitTypeReads(root *sitter.Node, buf []byte, repoID, relPath, fromSym string, out *ParseResult) {
	if root == nil || out == nil {
		return
	}
	seen := map[string]bool{}
	Walk(root, func(n *sitter.Node) {
		if n == nil {
			return
		}
		switch n.Type() {
		case "type_identifier", "identifier":
		default:
			return
		}
		tok := strings.TrimSpace(n.Content(buf))
		if tok == "" || tok[0] < 'A' || tok[0] > 'Z' || seen[tok] {
			return
		}
		if cppSkipType(tok) || cppSkipCallable(tok) {
			return
		}
		seen[tok] = true
		tgt := fmt.Sprintf("symref:%s:%s:%s", repoID, relPath, tok)
		out.Edges = append(out.Edges, types.Reference{
			ID:         edgeID(repoID, fromSym, tgt, "reads"),
			RepoID:     repoID,
			Kind:       types.RefKindReads,
			SourceID:   fromSym,
			TargetID:   tgt,
			Confidence: 0.55,
		})
	})
}

// cppFuncName returns the bare function/method name and optional Class qualifier
// for out-of-line definitions (Engine::tick → name=tick, qual=Engine).
func cppFuncName(decl *sitter.Node, buf []byte) (name, qual string) {
	if decl == nil {
		return "", ""
	}
	// Prefer walking into function_declarator when present.
	if decl.Type() != "function_declarator" {
		if fd := decl.ChildByFieldName("declarator"); fd != nil {
			return cppFuncName(fd, buf)
		}
		for i := 0; i < int(decl.NamedChildCount()); i++ {
			c := decl.NamedChild(i)
			if c != nil && c.Type() == "function_declarator" {
				return cppFuncName(c, buf)
			}
		}
	}
	for i := 0; i < int(decl.NamedChildCount()); i++ {
		c := decl.NamedChild(i)
		if c == nil {
			continue
		}
		switch c.Type() {
		case "qualified_identifier":
			var ns, id string
			for j := 0; j < int(c.NamedChildCount()); j++ {
				ch := c.NamedChild(j)
				if ch == nil {
					continue
				}
				switch ch.Type() {
				case "namespace_identifier", "type_identifier", "identifier":
					if ns == "" && id == "" {
						ns = strings.TrimSpace(ch.Content(buf))
					} else {
						id = strings.TrimSpace(ch.Content(buf))
					}
				case "field_identifier":
					id = strings.TrimSpace(ch.Content(buf))
				}
			}
			if id != "" {
				return id, ns
			}
		case "field_identifier", "identifier", "destructor_name":
			return strings.TrimSpace(c.Content(buf)), ""
		}
	}
	return FirstIdentifier(decl, buf), ""
}
