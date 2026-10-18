package tools

import (
	"context"
	"sync"
)

// testTurnScopes binds a turn scope to a fixture registry so package tests can
// keep calling callTool without threading a context through every call site.
// Production code never reads this map.
var testTurnScopes sync.Map

func bindTestTurn(r *Registry, scope *TurnScope) *TurnScope {
	testTurnScopes.Store(r, scope)
	return scope
}

func testTurnScopeFor(r *Registry) *TurnScope {
	if scope, ok := testTurnScopes.Load(r); ok {
		return scope.(*TurnScope)
	}
	return nil
}

func testTurnCtx(r *Registry) context.Context {
	return WithTurnScope(context.Background(), testTurnScopeFor(r))
}

func scopedCtx(scope *TurnScope) context.Context {
	return WithTurnScope(context.Background(), scope)
}
