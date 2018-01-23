// Copyright 2013 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// This file implements typechecking of call and selector expressions.

package types

import (
	"github.com/gijit/gi/pkg/ast"
	"github.com/gijit/gi/pkg/token"
)

func (check *Checker) call(x *operand, e *ast.CallExpr) exprKind {
	switch x.typ.Underlying().(type) {
	case *Signature:
		pp("Checker.call called with e = '%s', x = '%#v', sig='%s'", e, x, x.typ.Underlying().(*Signature))
	}
	check.exprOrType(x, e.Fun)

	switch x.mode {
	case invalid:
		check.use(e.Args...)
		x.mode = invalid
		x.expr = e
		return statement

	case typexpr:
		// conversion
		T := x.typ
		x.mode = invalid
		switch n := len(e.Args); n {
		case 0:
			check.errorf(e.Rparen, "missing argument in conversion to %s", T)
		case 1:
			check.expr(x, e.Args[0])
			if x.mode != invalid {
				check.conversion(x, T)
			}
		default:
			check.errorf(e.Args[n-1].Pos(), "too many arguments in conversion to %s", T)
		}
		x.expr = e
		return conversion

	case builtin:
		id := x.id
		if !check.builtin(x, e, id) {
			x.mode = invalid
		}
		x.expr = e
		// a non-constant result implies a function call
		if x.mode != invalid && x.mode != constant_ {
			check.hasCallOrRecv = true
		}
		return predeclaredFuncs[id].kind

	default:
		// function/method call
		sig, _ := x.typ.Underlying().(*Signature)
		pp("on redef, sig is wrong here. got sig = '%s'", sig)
		pp("x.typ = '%#v'", x.typ.Underlying())
		pp("x.typ.Underlying() = '%#v'", x.typ.Underlying())
		if sig == nil {
			check.invalidOp(x.pos(), "cannot call non-function %s", x)
			x.mode = invalid
			x.expr = e
			return statement
		}

		arg, n, _ := unpack(func(x *operand, i int) { check.multiExpr(x, e.Args[i]) }, len(e.Args), false)
		if arg != nil {
			pp("before check.aruments(), in call.go arg = '%#v'", arg)
			check.arguments(x, e, sig, arg, n)
		} else {
			x.mode = invalid
		}

		// determine result
		switch sig.results.Len() {
		case 0:
			x.mode = novalue
		case 1:
			x.mode = value
			x.typ = sig.results.vars[0].typ // unpack tuple
		default:
			x.mode = value
			x.typ = sig.results
		}

		x.expr = e
		check.hasCallOrRecv = true

		return statement
	}
}

// use type-checks each argument.
// Useful to make sure expressions are evaluated
// (and variables are "used") in the presence of other errors.
func (check *Checker) use(arg ...ast.Expr) {
	var x operand
	for _, e := range arg {
		if e != nil { // be safe
			check.rawExpr(&x, e, nil)
		}
	}
}

// useGetter is like use, but takes a getter instead of a list of expressions.
// It should be called instead of use if a getter is present to avoid repeated
// evaluation of the first argument (since the getter was likely obtained via
// unpack, which may have evaluated the first argument already).
func (check *Checker) useGetter(get getter, n int) {
	var x operand
	for i := 0; i < n; i++ {
		get(&x, i)
	}
}

// A getter sets x as the i'th operand, where 0 <= i < n and n is the total
// number of operands (context-specific, and maintained elsewhere). A getter
// type-checks the i'th operand; the details of the actual check are getter-
// specific.
type getter func(x *operand, i int)

// unpack takes a getter get and a number of operands n. If n == 1, unpack
// calls the incoming getter for the first operand. If that operand is
// invalid, unpack returns (nil, 0, false). Otherwise, if that operand is a
// function call, or a comma-ok expression and allowCommaOk is set, the result
// is a new getter and operand count providing access to the function results,
// or comma-ok values, respectively. The third result value reports if it
// is indeed the comma-ok case. In all other cases, the incoming getter and
// operand count are returned unchanged, and the third result value is false.
//
// In other words, if there's exactly one operand that - after type-checking
// by calling get - stands for multiple operands, the resulting getter provides
// access to those operands instead.
//
// If the returned getter is called at most once for a given operand index i
// (including i == 0), that operand is guaranteed to cause only one call of
// the incoming getter with that i.
//
func unpack(get getter, n int, allowCommaOk bool) (getter, int, bool) {
	if n != 1 {
		// zero or multiple values
		return get, n, false
	}
	// possibly result of an n-valued function call or comma,ok value
	var x0 operand
	get(&x0, 0)
	if x0.mode == invalid {
		return nil, 0, false
	}

	if t, ok := x0.typ.(*Tuple); ok {
		// result of an n-valued function call
		return func(x *operand, i int) {
			x.mode = value
			x.expr = x0.expr
			x.typ = t.At(i).typ
		}, t.Len(), false
	}

	if x0.mode == mapindex || x0.mode == commaok {
		// comma-ok value
		if allowCommaOk {
			a := [2]Type{x0.typ, Typ[UntypedBool]}
			return func(x *operand, i int) {
				x.mode = value
				x.expr = x0.expr
				x.typ = a[i]
			}, 2, true
		}
		x0.mode = value
	}

	// single value
	return func(x *operand, i int) {
		if i != 0 {
			unreachable()
		}
		*x = x0
	}, 1, false
}

// arguments checks argument passing for the call with the given signature.
// The arg function provides the operand for the i'th argument.
func (check *Checker) arguments(x *operand, call *ast.CallExpr, sig *Signature, arg getter, n int) {
	pp("top of Checker.arguments: sig = '%s'", sig)
	if call.Ellipsis.IsValid() {
		// last argument is of the form x...
		if !sig.variadic {
			check.errorf(call.Ellipsis, "cannot use ... in call to non-variadic %s", call.Fun)
			check.useGetter(arg, n)
			return
		}
		if len(call.Args) == 1 && n > 1 {
			// f()... is not permitted if f() is multi-valued
			check.errorf(call.Ellipsis, "cannot use ... with %d-valued %s", n, call.Args[0])
			check.useGetter(arg, n)
			return
		}
	}

	// evaluate arguments
	for i := 0; i < n; i++ {
		arg(x, i)
		if x.mode != invalid {
			var ellipsis token.Pos
			if i == n-1 && call.Ellipsis.IsValid() {
				ellipsis = call.Ellipsis
			}
			check.argument(call.Fun, sig, i, x, ellipsis)
		}
	}

	// check argument count
	if sig.variadic {
		// a variadic function accepts an "empty"
		// last argument: count one extra
		n++
	}
	if n < sig.params.Len() {
		check.errorf(call.Rparen, "too few arguments in call to %s", call.Fun)
		// ok to continue
	}
}

// argument checks passing of argument x to the i'th parameter of the given signature.
// If ellipsis is valid, the argument is followed by ... at that position in the call.
func (check *Checker) argument(fun ast.Expr, sig *Signature, i int, x *operand, ellipsis token.Pos) {
	pp("check.Checker argument top: sig = '%s'", sig.String())
	check.singleValue(x)
	if x.mode == invalid {
		return
	}

	n := sig.params.Len()

	// determine parameter type
	var typ Type
	switch {
	case i < n:
		typ = sig.params.vars[i].typ
	case sig.variadic:
		typ = sig.params.vars[n-1].typ
		if debug {
			if _, ok := typ.(*Slice); !ok {
				check.dump("%s: expected unnamed slice type, got %s", sig.params.vars[n-1].Pos(), typ)
			}
		}
	default:
		// jea: after re-defining a method in 069 repl_test, and
		// trying to call with the new method that has 1 more arg,
		// we are failing here.
		check.errorf(x.pos(), "too many arguments")
		return
	}

	if ellipsis.IsValid() {
		// argument is of the form x... and x is single-valued
		if i != n-1 {
			check.errorf(ellipsis, "can only use ... with matching parameter")
			return
		}
		if _, ok := x.typ.Underlying().(*Slice); !ok && x.typ != Typ[UntypedNil] { // see issue #18268
			check.errorf(x.pos(), "cannot use %s as parameter of type %s", x, typ)
			return
		}
	} else if sig.variadic && i >= n-1 {
		// use the variadic parameter slice's element type
		typ = typ.(*Slice).elem
	}

	check.assignment(x, typ, check.sprintf("argument to %s", fun))
}

func (check *Checker) selector(x *operand, e *ast.SelectorExpr) {
	// these must be declared before the "goto Error" statements
	var (
		obj      Object
		index    []int
		indirect bool
	)

	sel := e.Sel.Name
	pp("jea debug: check.selector top. sel='%s'", sel)

	// If the identifier refers to a package, handle everything here
	// so we don't need a "package" mode for operands: package names
	// can only appear in qualified identifiers which are mapped to
	// selector expressions.
	if check.scope == nil {
		// yep, this is the problem!
		pp("jea debug problem! check.scope is nil in call.go:283, check.selector()")
		//check.scope.Dump()
		panic("scope should not be nil!")
	} else {
		pp("call.go:282 we think fmt.Sprintf lookup is failing b/c check.scope isn't set right. check.scope = %p = '%s', with %v children", check.scope, check.scope, len(check.scope.children))
	}
	if ident, ok := e.X.(*ast.Ident); ok {
		//pp("here!! jea: this is the lookup that is going wrong for out-of-date s.inc call")
		//check.scope.Dump()
		_, obj := check.scope.LookupParent(ident.Name, check.pos)

		pp("jea debug: identifier refers to a package or struct? ok was true, ident.Name='%#v', sel='%#v', obj='%#v'", ident.Name, sel, obj) // ident.Name="s", sel="inc", obj='&types.Var{object:types.object{parent:(*types.Scope)(0xc4200608a0), pos:215, pkg:(*types.Package)(0xc4200b8690), name:"s", typ:types.Type(nil), order_:0x4, scopePos_:0}, anonymous:false, visited:false, isField:false, used:false}'

		if pname, _ := obj.(*PkgName); pname != nil {
			pp("inside the obj was *PkgName path...") // fmt.Sprintf not getting here, obj nil.
			assert(pname.pkg == check.pkg)
			check.recordUse(ident, pname)
			pname.used = true
			pkg := pname.imported
			exp := pkg.scope.Lookup(sel)
			if exp == nil {
				if !pkg.fake {
					check.errorf(e.Pos(), "%s not declared by package %s", sel, pkg.name)
				}
				goto Error
			}
			if !exp.Exported() {
				check.errorf(e.Pos(), "%s not exported by package %s", sel, pkg.name)
				// ok to continue
			}
			check.recordUse(e.Sel, exp)

			// Simplified version of the code for *ast.Idents:
			// - imported objects are always fully initialized
			switch exp := exp.(type) {
			case *Const:
				assert(exp.Val() != nil)
				x.mode = constant_
				x.typ = exp.typ
				x.val = exp.val
			case *TypeName:
				x.mode = typexpr
				x.typ = exp.typ
			case *Var:
				x.mode = variable
				x.typ = exp.typ
			case *Func:
				x.mode = value
				x.typ = exp.typ
				pp("wrote x.typ = exp.typ = '%#v'", exp.typ)
			case *Builtin:
				x.mode = builtin
				x.typ = exp.typ
				x.id = exp.id
			default:
				check.dump("unexpected object %v", exp)
				unreachable()
			}
			x.expr = e
			return
		}
	}

	check.exprOrType(x, e.X)
	if x.mode == invalid {
		goto Error
	}

	obj, index, indirect = LookupFieldOrMethod(x.typ, x.mode == variable, check.pkg, sel)
	if obj == nil {
		switch {
		case index != nil:
			// TODO(gri) should provide actual type where the conflict happens
			check.invalidOp(e.Pos(), "ambiguous selector %s", sel)
		case indirect:
			check.invalidOp(e.Pos(), "%s is not in method set of %s", sel, x.typ)
		default:
			check.invalidOp(e.Pos(), "%s has no field or method %s", x, sel)
		}
		goto Error
	}

	if x.mode == typexpr {
		// method expression
		m, _ := obj.(*Func)
		if m == nil {
			check.invalidOp(e.Pos(), "%s has no method %s", x, sel)
			goto Error
		}

		check.recordSelection(e, MethodExpr, x.typ, m, index, indirect)

		// the receiver type becomes the type of the first function
		// argument of the method expression's function type
		var params []*Var
		sig := m.typ.(*Signature)
		if sig.params != nil {
			params = sig.params.vars
		}
		x.mode = value
		pp("jea debug: about to make new x.typ Signature!: params='%#v'\n", params)
		x.typ = &Signature{
			params:   NewTuple(append([]*Var{NewVar(token.NoPos, check.pkg, "", x.typ)}, params...)...),
			results:  sig.results,
			variadic: sig.variadic,
		}

		check.addDeclDep(m)

	} else {
		// regular selector
		switch obj := obj.(type) {
		case *Var:
			check.recordSelection(e, FieldVal, x.typ, obj, index, indirect)
			if x.mode == variable || indirect {
				x.mode = variable
			} else {
				x.mode = value
			}
			x.typ = obj.typ

		case *Func:
			// TODO(gri) If we needed to take into account the receiver's
			// addressability, should we report the type &(x.typ) instead?
			pp("prior to recordSelection, x.typ='%#v", x.typ)
			check.recordSelection(e, MethodVal, x.typ, obj, index, indirect)

			if debug {
				// Verify that LookupFieldOrMethod and MethodSet.Lookup agree.
				typ := x.typ
				if x.mode == variable {
					// If typ is not an (unnamed) pointer or an interface,
					// use *typ instead, because the method set of *typ
					// includes the methods of typ.
					// Variables are addressable, so we can always take their
					// address.
					if _, ok := typ.(*Pointer); !ok && !IsInterface(typ) {
						typ = &Pointer{base: typ}
					}
				}
				// If we created a synthetic pointer type above, we will throw
				// away the method set computed here after use.
				// TODO(gri) Method set computation should probably always compute
				// both, the value and the pointer receiver method set and represent
				// them in a single structure.
				// TODO(gri) Consider also using a method set cache for the lifetime
				// of checker once we rely on MethodSet lookup instead of individual
				// lookup.
				mset := NewMethodSet(typ)
				if m := mset.Lookup(check.pkg, sel); m == nil || m.obj != obj {
					pp("sel='%v'; m == nil? : %v", sel, m == nil) // true here
					check.dump("e.Pos(): %s: (%s).%v -> %s", e.Pos(), typ, obj.name, m)
					check.dump("mset: %s\n", mset)
					// jea debug
					panic("method sets and lookup don't agree")
				}
			} // end debug

			x.mode = value

			// remove receiver
			sig := *obj.typ.(*Signature)
			sig.recv = nil
			x.typ = &sig

			check.addDeclDep(obj)

		default:
			unreachable()
		}
	}

	// everything went well
	x.expr = e
	return

Error:
	x.mode = invalid
	x.expr = e
}
