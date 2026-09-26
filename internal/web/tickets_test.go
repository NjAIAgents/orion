package web

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// stubTickets is a tracker that never authenticated at all. That it satisfies
// the interface is the point: the board's demand on a tracker is a lookup,
// and a lookup has no credential in it.
type stubTickets struct{ titles map[string]string }

func (s stubTickets) Ticket(key string) (Ticket, error) {
	return Ticket{Key: key, Title: s.titles[key], Stage: "In Progress"}, nil
}

// A COMPILE-TIME assertion, and it is the strongest half of OR-278: if anyone
// widens Tickets so a method takes a credential -- a token argument, a
// config to read one out of -- this stops building, before any test runs.
var _ Tickets = stubTickets{}

// The seam is used by handing over an already-built client. Nothing on the
// call path names a credential, so there is nothing for the board to keep.
func TestTicketsIsSatisfiedByAnAlreadyAuthenticatedClient(t *testing.T) {
	var tickets Tickets = stubTickets{titles: map[string]string{"OR-278": "The tracker credential never enters the scanned data model"}}

	got, err := tickets.Ticket("OR-278")
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "The tracker credential never enters the scanned data model" {
		t.Errorf("Ticket.Title = %q, want the tracker's summary", got.Title)
	}
	if got.Stage != "In Progress" {
		t.Errorf("Ticket.Stage = %q, want the tracker's own status word", got.Stage)
	}
}

// A Ticket fills the tracker's half of a card and nothing else. If it ever
// grows a field the page does not draw -- a client, a config, a credential --
// this is where it shows up.
func TestTicketCarriesOnlyWhatACardDraws(t *testing.T) {
	want := map[string]bool{"Key": true, "Title": true, "Stage": true}

	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "tickets.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}

	found := map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name.Name != "Ticket" {
			return true
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok {
			return true
		}
		for _, field := range st.Fields.List {
			for _, name := range field.Names {
				found[name.Name] = true
				if !want[name.Name] {
					t.Errorf("Ticket has an unexpected field %q.\n"+
						"  OR-278: the board's tracker record is narrow on purpose -- a field\n"+
						"  the page does not draw is a field that carries the tracker's own\n"+
						"  state, and a client, into the model web.Scan renders.", name.Name)
				}
				if strings.Contains(strings.ToLower(name.Name), "token") {
					t.Errorf("Ticket.%s: the credential is resolved at process start and never reaches this type", name.Name)
				}
			}
		}
		return false
	})

	for name := range want {
		if !found[name] {
			t.Errorf("Ticket is missing %q, which a card needs from the tracker", name)
		}
	}
}
