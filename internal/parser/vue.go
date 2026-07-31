package parser

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/VeyrForge/codehelper/pkg/types"
)

// Vue template event handlers: @click="fn" / @click="fn()" / v-on:click="fn".
var vueEventHandlerRe = regexp.MustCompile(`(?i)(?:@[\w.-]+|v-on:[\w.-]+)\s*=\s*["']\s*([A-Za-z_$][\w$]*)`)

// Vue compiler macros in <script setup> (optional TS type args before '(').
var vueMacroRe = regexp.MustCompile(`\b(defineProps|defineEmits|defineExpose|defineModel|defineOptions|defineSlots|withDefaults)\b`)

// Script bindings: const count = ref(...) / const doubled = computed(...).
var vueRefComputedBindRe = regexp.MustCompile(
	`(?m)^\s*(?:export\s+)?(?:const|let|var)\s+([A-Za-z_$][\w$]*)\s*=\s*` +
		`(ref|computed|shallowRef|shallowReactive|reactive|toRef|readonly)\s*(?:<[^>]*>)?\s*\(`,
)

// Template mustache: {{ count }} / {{ count.value }} — first identifier only.
var vueMustacheIdentRe = regexp.MustCompile(`\{\{\s*([A-Za-z_$][\w$]*)`)

// Directive / v-bind / v-model simple identifier RHS (not full expressions).
var vueTemplateBindingIdentRe = regexp.MustCompile(
	`(?i)(?::[\w.-]+|v-bind:[\w.-]+|v-model(?::[\w.-]+)?|v-if|v-else-if|v-show|v-html|v-text)\s*=\s*["']\s*([A-Za-z_$][\w$]*)`,
)

var vueTemplateSkipIdents = map[string]bool{
	"true": true, "false": true, "null": true, "undefined": true, "NaN": true,
	"this": true, "props": true, "emit": true, "slots": true, "attrs": true,
	"console": true, "window": true, "document": true, "Math": true, "JSON": true,
	"String": true, "Number": true, "Boolean": true, "Array": true, "Object": true,
	"Date": true, "Error": true, "Promise": true, "Map": true, "Set": true,
}

// ParseVue densifies .vue SFCs: script(+setup) via TS/JS, markup component tags,
// style classes, @/v-on event→handler edges, define* macros, and template
// reads of ref/computed bindings. Full template expression / reactivity graphs
// (watchEffect dep nets, .value chains) are not claimed.
func ParseVue(ctx context.Context, repoID, relPath string, buf []byte) (*ParseResult, error) {
	out := &ParseResult{}
	text := string(buf)

	comp, compID := sfcComponentSymbol(repoID, relPath, "vue")
	if compID != "" {
		out.Symbols = append(out.Symbols, comp)
		out.Edges = append(out.Edges, containsEdge(repoID, relPath, comp.ID))
	}

	extractSFCScripts(ctx, out, repoID, relPath, text, "vue")
	extractSFCMarkup(out, repoID, relPath, text, compID, "vue")
	extractSFCStyle(out, repoID, relPath, text, compID, "vue")
	extractVueEventsAndMacros(out, repoID, relPath, text, compID)
	extractVueRefComputedAndTemplate(out, repoID, relPath, text, compID)
	return out, nil
}

func extractVueEventsAndMacros(out *ParseResult, repoID, relPath, text, compID string) {
	if out == nil {
		return
	}
	markup := stripSFCBlocks(text)
	seenEvt := map[string]bool{}
	for _, m := range vueEventHandlerRe.FindAllStringSubmatch(markup, -1) {
		if len(m) < 2 {
			continue
		}
		handler := m[1]
		if handler == "" || seenEvt[handler] {
			continue
		}
		seenEvt[handler] = true
		if compID != "" {
			tgt := fmt.Sprintf("symref:%s:%s:%s", repoID, relPath, handler)
			out.Edges = append(out.Edges, types.Reference{
				ID:         edgeID(repoID, compID, tgt, "calls"),
				RepoID:     repoID,
				Kind:       types.RefKindCalls,
				SourceID:   compID,
				TargetID:   tgt,
				Confidence: 0.8,
			})
		}
	}
	for _, sm := range sfcScriptRe.FindAllStringSubmatchIndex(text, -1) {
		if len(sm) < 6 {
			continue
		}
		bodyStart, bodyEnd := sm[4], sm[5]
		body := text[bodyStart:bodyEnd]
		lineOffset := 1 + strings.Count(text[:bodyStart], "\n")
		seen := map[string]bool{}
		for _, rm := range vueMacroRe.FindAllStringSubmatchIndex(body, -1) {
			if len(rm) < 4 {
				continue
			}
			name := body[rm[2]:rm[3]]
			if name == "" || seen[name] {
				continue
			}
			seen[name] = true
			line := lineOffset + strings.Count(body[:rm[2]], "\n")
			sym := symbol(repoID, relPath, name, types.SymbolKindFunction, line, line, "vue", "macro", "")
			out.Symbols = append(out.Symbols, sym)
			out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
			if compID != "" {
				out.Edges = append(out.Edges, types.Reference{
					ID:         edgeID(repoID, compID, sym.ID, "calls"),
					RepoID:     repoID,
					Kind:       types.RefKindCalls,
					SourceID:   compID,
					TargetID:   sym.ID,
					Confidence: 0.85,
				})
			}
		}
	}
}

// extractVueRefComputedAndTemplate indexes ref/computed(/reactive) bindings and
// emits component→ident reads for simple template mustaches and directive RHS.
func extractVueRefComputedAndTemplate(out *ParseResult, repoID, relPath, text, compID string) {
	if out == nil {
		return
	}
	existingByName := map[string]string{} // name → symbol ID
	for _, s := range out.Symbols {
		if s.Name != "" && existingByName[s.Name] == "" {
			existingByName[s.Name] = s.ID
		}
	}
	seenBind := map[string]bool{}
	for _, sm := range sfcScriptRe.FindAllStringSubmatchIndex(text, -1) {
		if len(sm) < 6 {
			continue
		}
		bodyStart, bodyEnd := sm[4], sm[5]
		body := text[bodyStart:bodyEnd]
		lineOffset := 1 + strings.Count(text[:bodyStart], "\n")
		for _, bm := range vueRefComputedBindRe.FindAllStringSubmatchIndex(body, -1) {
			if len(bm) < 6 {
				continue
			}
			name := body[bm[2]:bm[3]]
			api := body[bm[4]:bm[5]]
			if name == "" || seenBind[name] {
				continue
			}
			seenBind[name] = true
			line := lineOffset + strings.Count(body[:bm[2]], "\n")
			sig := "vue-" + api
			symID := existingByName[name]
			if symID == "" {
				sym := symbol(repoID, relPath, name, types.SymbolKindVariable, line, line, "vue", sig, "")
				out.Symbols = append(out.Symbols, sym)
				out.Edges = append(out.Edges, containsEdge(repoID, relPath, sym.ID))
				symID = sym.ID
				existingByName[name] = symID
			} else {
				for i := range out.Symbols {
					if out.Symbols[i].ID == symID && out.Symbols[i].Signature == "" {
						out.Symbols[i].Signature = sig
						break
					}
					if out.Symbols[i].ID == symID && !strings.HasPrefix(out.Symbols[i].Signature, "vue-") {
						out.Symbols[i].Signature = sig
						break
					}
				}
			}
			apiTgt := fmt.Sprintf("symref:%s:%s:%s", repoID, relPath, api)
			out.Edges = append(out.Edges, types.Reference{
				ID:         edgeID(repoID, symID, apiTgt, "calls"),
				RepoID:     repoID,
				Kind:       types.RefKindCalls,
				SourceID:   symID,
				TargetID:   apiTgt,
				Confidence: 0.85,
			})
			if compID != "" {
				out.Edges = append(out.Edges, types.Reference{
					ID:         edgeID(repoID, compID, symID, "reads"),
					RepoID:     repoID,
					Kind:       types.RefKindReads,
					SourceID:   compID,
					TargetID:   symID,
					Confidence: 0.8,
				})
			}
		}
	}

	if compID == "" {
		return
	}
	markup := stripSFCBlocks(text)
	seenRead := map[string]bool{}
	addRead := func(ident string) {
		if ident == "" || vueTemplateSkipIdents[ident] || seenRead[ident] {
			return
		}
		seenRead[ident] = true
		if id := existingByName[ident]; id != "" {
			out.Edges = append(out.Edges, types.Reference{
				ID:         edgeID(repoID, compID, id, "reads"),
				RepoID:     repoID,
				Kind:       types.RefKindReads,
				SourceID:   compID,
				TargetID:   id,
				Confidence: 0.75,
			})
			return
		}
		tgt := fmt.Sprintf("symref:%s:%s:%s", repoID, relPath, ident)
		out.Edges = append(out.Edges, types.Reference{
			ID:         edgeID(repoID, compID, tgt, "reads"),
			RepoID:     repoID,
			Kind:       types.RefKindReads,
			SourceID:   compID,
			TargetID:   tgt,
			Confidence: 0.7,
		})
	}
	for _, m := range vueMustacheIdentRe.FindAllStringSubmatch(markup, -1) {
		if len(m) >= 2 {
			addRead(m[1])
		}
	}
	for _, m := range vueTemplateBindingIdentRe.FindAllStringSubmatch(markup, -1) {
		if len(m) >= 2 {
			addRead(m[1])
		}
	}
}
