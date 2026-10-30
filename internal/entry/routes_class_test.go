package entry

import (
	"context"
	"go/ast"
	"go/types"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
	"golang.org/x/tools/go/packages"
)

// The class test of audit 42 recheck НВ1: the step-up stood on the verb
// `retire`, while two routes behind the owner gate alone reached the same
// effect — a slot's token (whose voice evicts machines) and a relay's death
// (whose idle machine the pass deletes). A test per route would cover the two
// that were found and none added later. So the package is read as source:
// every function's calls, closures included, and for each route mounted
// behind `gate` the set of sinks its handler reaches. A sink is what destroys
// without a way back, hands out a voice, or spends.
//
// # The graph is typed, not by name
//
// It used to be by name, and names collide: `Run` is an ssh command and the
// mechanic's timer loop, `run` both the provisioner's job body and the
// mechanic's repair (audit 42 third pass, НН-3-2). Through `run` the
// provision route "reached" every sink there is — the order, the handback,
// the hoster's delete — so its exemption had to cover the whole route, and a
// real new edge out of it would have gone unnoticed. Calls are resolved by
// go/types now: a method is its receiver's, an interface method reaches every
// type of this package that implements the interface, and a func-typed field
// or variable reaches every function stored in it. That keeps the graph an
// over-approximation where it must be one (dynamic dispatch) and exact where
// it can be.

// destroyingSinks are the functions whose effect a stolen owner session
// without a passkey touch must not reach.
var destroyingSinks = map[string]string{
	"Service.Retire":               "сдаёт машину",
	"Service.DeleteBoxInstance":    "удаляет машину у хостера",
	"Service.MarkDead":             "убивает релей — простаивающую машину сдаёт idle-проход",
	"Service.ForgetHostKey":        "следующий вход доверяет любому ключу",
	"jobRun.wipe":                  "стирает машину",
	"jobRun.autoHandBack":          "сдаёт и удаляет машину",
	"Service.CreateVantage":        "выдаёт голос точки наблюдения",
	"Service.issueBoxVantageToken": "выдаёт голос точки наблюдения",
	"Service.IssueVantageToken":    "выдаёт голос точки наблюдения",
	"Service.reserveOrder":         "заказывает машину",
	"Service.purchase":             "покупает домен",
	"Service.SaveProvider":         "меняет ключи провайдера",
	"Service.SaveDetectionPolicy":  "меняет пороги выселения",
	"Service.SetPlaybookMode":      "включает траты без кнопки",
	"Service.LiftPause":            "снимает предохранитель волны",
}

// gateAllowed are the (route, sink) pairs behind the gate alone, each with the
// owner's reason. Adding one is a decision, not a fix; a pair excuses that
// sink on that route and nothing else.
var gateAllowed = map[string]map[string]string{
	// §8.2 of the recheck (27.10): provisioning stays on the gate; a domain
	// it buys obeys the buy_domain mode and pause (ensureDomain). The port
	// burn (MarkDead) this exemption once named was the graph by name's: no
	// edge from provision reaches Service.MarkDead.
	"POST /api/v1/admin/entry/boxes/{name}/provision": {
		"Service.purchase": "решение владельца 27.10 §8.2",
	},
}

func TestNoGateRouteReachesADestroyingSink(t *testing.T) {
	g := readPackageGraph(t)
	for sink := range destroyingSinks {
		if !g.funcs[sink] {
			t.Errorf("sink %s is not a function of the package any more", sink)
		}
	}
	if len(g.routes) < 40 {
		t.Fatalf("read %d routes from RegisterAdminRoutes — the reader lost the shape", len(g.routes))
	}
	var bad []string
	for _, r := range g.routes {
		if r.wrap != "gate" {
			continue
		}
		seen := g.reach(r.handler)
		var hit []string
		for sink, what := range destroyingSinks {
			if seen[sink] && gateAllowed[r.pattern][sink] == "" {
				hit = append(hit, sink+" ("+what+")")
			}
		}
		if len(hit) == 0 {
			continue
		}
		sort.Strings(hit)
		bad = append(bad, r.pattern+" → "+strings.Join(hit, ", "))
	}
	sort.Strings(bad)
	for _, b := range bad {
		t.Errorf("behind the gate alone: %s", b)
	}
	// The allowlist must not outlive the route or the edge it excuses.
	for pattern, sinks := range gateAllowed {
		var route *mountedRoute
		for i, r := range g.routes {
			if r.pattern == pattern && r.wrap == "gate" {
				route = &g.routes[i]
			}
		}
		if route == nil {
			t.Errorf("gateAllowed names %s, which is not a gate route any more", pattern)
			continue
		}
		seen := g.reach(route.handler)
		for sink := range sinks {
			if !seen[sink] {
				t.Errorf("gateAllowed excuses %s on %s, which no longer reaches it", sink, pattern)
			}
		}
	}
}

// The provision route reaches what §8.2 allows it and nothing else. Under the
// graph by name it reached the order, the handback and the hoster's delete
// through the name `run` (НН-3-2) — the test that proves the graph is exact
// enough for the pairs above to mean something.
func TestTheProvisionRouteReachesOnlyWhatTheOwnerAllowed(t *testing.T) {
	g := readPackageGraph(t)
	seen := g.reach("AdminHandlers.ProvisionBox")
	if !seen["Provisioner.run"] || seen["Mechanic.run"] {
		t.Fatalf("Provisioner.run reached: %v, Mechanic.run reached: %v — the graph does not tell the two apart",
			seen["Provisioner.run"], seen["Mechanic.run"])
	}
	for sink := range destroyingSinks {
		allowed := gateAllowed["POST /api/v1/admin/entry/boxes/{name}/provision"][sink] != ""
		if seen[sink] != allowed {
			t.Errorf("provision reaches %s: %v, the owner allowed it: %v", sink, seen[sink], allowed)
		}
	}
}

type mountedRoute struct{ pattern, wrap, handler string }

// packageGraph is the package's non-test functions as go/types sees them,
// keyed "Recv.Method" or "Func", with what each may call, and the routes
// RegisterAdminRoutes mounts.
type packageGraph struct {
	funcs  map[string]bool
	calls  map[string]map[string]bool
	routes []mountedRoute
}

func (g packageGraph) reach(root string) map[string]bool {
	seen := map[string]bool{}
	stack := []string{root}
	for len(stack) > 0 {
		f := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[f] {
			continue
		}
		seen[f] = true
		for c := range g.calls[f] {
			stack = append(stack, c)
		}
	}
	return seen
}

// readPackageGraph type-checks the package (its non-test sources) and builds
// the call graph: every reference to a function or method in a function's
// body — a call, or a method value passed along (h.MarkDead, p.runOrdered),
// which is a call made later — is an edge; an interface method fans out to
// this package's implementations; a func-typed field or variable is a node of
// its own, reached by the functions that call it and reaching every function
// stored in it.
func readPackageGraph(t *testing.T) packageGraph {
	t.Helper()
	pkgs, err := packages.Load(&packages.Config{
		Mode: packages.NeedName | packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo,
	}, ".")
	if err != nil {
		t.Fatal(err)
	}
	if len(pkgs) != 1 || len(pkgs[0].Errors) > 0 {
		t.Fatalf("load the package: %v", pkgs)
	}
	pkg := pkgs[0]
	info := pkg.TypesInfo
	local := func(o types.Object) bool { return o.Pkg() == pkg.Types }
	key := func(f *types.Func) string {
		f = f.Origin()
		name := f.Name()
		if recv := f.Type().(*types.Signature).Recv(); recv != nil {
			rt := recv.Type()
			if p, ok := rt.(*types.Pointer); ok {
				rt = p.Elem()
			}
			if n, ok := rt.(*types.Named); ok {
				name = n.Obj().Name() + "." + name
			}
		}
		switch {
		case f.Pkg() == nil:
			name = "universe." + name // error.Error
		case !local(f):
			name = f.Pkg().Path() + "." + name
		}
		return name
	}
	varKey := func(v *types.Var) string {
		return "var " + v.Pkg().Path() + "." + v.Name() + "@" + pkg.Fset.Position(v.Pos()).String()
	}
	isFuncVar := func(o types.Object) (*types.Var, bool) {
		v, ok := o.(*types.Var)
		if !ok || !local(v) {
			return nil, false
		}
		_, sig := v.Type().Underlying().(*types.Signature)
		return v, sig
	}
	g := packageGraph{funcs: map[string]bool{}, calls: map[string]map[string]bool{}}
	edge := func(from, to string) {
		if g.calls[from] == nil {
			g.calls[from] = map[string]bool{}
		}
		g.calls[from][to] = true
	}
	// funcRefs is every function an expression refers to.
	funcRefs := func(e ast.Expr) []string {
		var out []string
		ast.Inspect(e, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok {
				if f, ok := info.Uses[id].(*types.Func); ok {
					out = append(out, key(f))
				}
			}
			return true
		})
		return out
	}
	var abstract []*types.Func
	for _, file := range pkg.Syntax {
		for _, d := range file.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			self := key(info.Defs[fd.Name].(*types.Func))
			g.funcs[self] = true
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.Ident:
					switch o := info.Uses[x].(type) {
					case *types.Func:
						edge(self, key(o))
						if recv := o.Type().(*types.Signature).Recv(); recv != nil {
							if _, ok := recv.Type().Underlying().(*types.Interface); ok {
								abstract = append(abstract, o)
							}
						}
					case *types.Var:
						if v, ok := isFuncVar(o); ok {
							edge(self, varKey(v))
						}
					}
				case *ast.AssignStmt:
					for i, lhs := range x.Lhs {
						if i >= len(x.Rhs) {
							break
						}
						id, ok := lhs.(*ast.Ident)
						if sel, isSel := lhs.(*ast.SelectorExpr); isSel {
							id, ok = sel.Sel, true
						}
						if !ok {
							continue
						}
						obj := info.Uses[id]
						if obj == nil {
							obj = info.Defs[id]
						}
						if v, ok := isFuncVar(obj); ok {
							for _, f := range funcRefs(x.Rhs[i]) {
								edge(varKey(v), f)
							}
						}
					}
				case *ast.KeyValueExpr:
					if id, ok := x.Key.(*ast.Ident); ok {
						if v, ok := isFuncVar(info.Uses[id]); ok {
							for _, f := range funcRefs(x.Value) {
								edge(varKey(v), f)
							}
						}
					}
				}
				return true
			})
			if fd.Name.Name == "RegisterAdminRoutes" {
				g.routes = mountedRoutes(fd, info, key)
			}
		}
	}
	// Dynamic dispatch: an interface method reaches every type of the
	// package that implements the interface.
	scope := pkg.Types.Scope()
	for _, m := range abstract {
		iface := m.Type().(*types.Signature).Recv().Type().Underlying().(*types.Interface)
		for _, name := range scope.Names() {
			tn, ok := scope.Lookup(name).(*types.TypeName)
			if !ok || types.IsInterface(tn.Type()) {
				continue
			}
			for _, t := range []types.Type{tn.Type(), types.NewPointer(tn.Type())} {
				if !types.Implements(t, iface) {
					continue
				}
				obj, _, _ := types.LookupFieldOrMethod(t, true, pkg.Types, m.Name())
				if f, ok := obj.(*types.Func); ok {
					edge(key(m), key(f))
				}
				break
			}
		}
	}
	return g
}

// mountedRoutes reads mux.HandleFunc("<pattern>", wrap(h.Handler)) calls.
func mountedRoutes(fd *ast.FuncDecl, info *types.Info, key func(*types.Func) string) []mountedRoute {
	var out []mountedRoute
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		c, ok := n.(*ast.CallExpr)
		if !ok || len(c.Args) != 2 {
			return true
		}
		if sel, ok := c.Fun.(*ast.SelectorExpr); !ok || sel.Sel.Name != "HandleFunc" {
			return true
		}
		lit, ok := c.Args[0].(*ast.BasicLit)
		if !ok {
			return true
		}
		wrapCall, ok := c.Args[1].(*ast.CallExpr)
		if !ok || len(wrapCall.Args) != 1 {
			return true
		}
		wrap, ok := wrapCall.Fun.(*ast.Ident)
		if !ok {
			return true
		}
		h, ok := wrapCall.Args[0].(*ast.SelectorExpr)
		if !ok {
			return true
		}
		f, ok := info.Uses[h.Sel].(*types.Func)
		if !ok {
			return true
		}
		out = append(out, mountedRoute{pattern: strings.Trim(lit.Value, `"`), wrap: wrap.Name, handler: key(f)})
		return true
	})
	return out
}

// Audit 42 recheck С5: 403 without a touch was tested for four routes, and
// none for DELETE …/instance, incidents/{id}/run, PUT policy, a pause's lift
// or the vantage routes. Every route mounted behind the step-up is asked here
// without one, with a body that would be accepted otherwise, and must answer
// 403 before its handler runs — the journal of admin actions stays empty.
func TestEveryStepUpRouteRefusesWithoutATouch(t *testing.T) {
	h := newHostHarness(t)
	routes := readPackageGraph(t).routes
	touched := false
	do := h.stepUpMux(&touched)
	var auditBefore int
	if err := h.pool.QueryRow(context.Background(), `SELECT count(*) FROM admin_audit_log`).Scan(&auditBefore); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, r := range routes {
		if r.wrap != "stepUp" {
			continue
		}
		n++
		method, path, _ := strings.Cut(r.pattern, " ")
		path = strings.NewReplacer("{name}", "zz1", "{id}", uuid.NewString(), "{cause}", CauseIPBanned,
			"{kind}", fakeHostKind, "{slot}", "zz", "{domain}", "example.org").Replace(path)
		if code, body := do(method, path, map[string]any{}); code != http.StatusForbidden {
			t.Errorf("%s without a touch: %d %s, want 403", r.pattern, code, body)
		}
	}
	if n < 20 {
		t.Fatalf("asked %d step-up routes — the reader lost the shape", n)
	}
	var auditAfter int
	if err := h.pool.QueryRow(context.Background(), `SELECT count(*) FROM admin_audit_log`).Scan(&auditAfter); err != nil {
		t.Fatal(err)
	}
	if auditAfter != auditBefore {
		t.Errorf("refused requests wrote %d audit rows", auditAfter-auditBefore)
	}
}
