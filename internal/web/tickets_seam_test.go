package web

import (
	"reflect"
	"testing"
)

// OR-278 case 11: the whole call path is a key in, a Ticket out -- nothing
// else. Checked by reflection on the interface method itself, not just on
// one stub's signature, so a second implementation with a wider method still
// fails to satisfy Tickets and this test still describes why.
func TestTicketMethodTakesOnlyAKeyAndReturnsOnlyATicket(t *testing.T) {
	method, ok := reflect.TypeOf((*Tickets)(nil)).Elem().MethodByName("Ticket")
	if !ok {
		t.Fatal("Tickets has no Ticket method")
	}

	// Interface method Type: no receiver in NumIn/NumOut.
	if got := method.Type.NumIn(); got != 1 {
		t.Fatalf("Tickets.Ticket takes %d arguments, want 1 (the key)", got)
	}
	if got := method.Type.In(0); got.Kind() != reflect.String {
		t.Fatalf("Tickets.Ticket's argument is %s, want string", got.Kind())
	}

	if got := method.Type.NumOut(); got != 2 {
		t.Fatalf("Tickets.Ticket returns %d values, want 2 (Ticket, error)", got)
	}
	if got := method.Type.Out(0); got != reflect.TypeOf(Ticket{}) {
		t.Fatalf("Tickets.Ticket's first return is %s, want web.Ticket", got)
	}
	errType := reflect.TypeOf((*error)(nil)).Elem()
	if got := method.Type.Out(1); got != errType {
		t.Fatalf("Tickets.Ticket's second return is %s, want error", got)
	}
}

// noCredentialTickets is a second, independent Tickets implementation built
// with zero fields -- there is nothing for a credential to occupy, and
// nothing in this test's scope names one.
type noCredentialTickets struct{}

func (noCredentialTickets) Ticket(key string) (Ticket, error) {
	return Ticket{Key: key}, nil
}

// OR-278 case 12: any concrete Tickets can be constructed with no credential
// in scope at the construction site. Two different implementations
// (stubTickets in tickets_test.go, and noCredentialTickets here) are built
// this way, so the property is about the interface, not about one stub.
func TestConcreteTicketsConstructWithoutACredentialInScope(t *testing.T) {
	var a Tickets = stubTickets{}
	var b Tickets = noCredentialTickets{}
	var c Tickets = &noCredentialTickets{}

	for name, tk := range map[string]Tickets{"stubTickets": a, "noCredentialTickets value": b, "noCredentialTickets pointer": c} {
		if _, err := tk.Ticket("OR-1"); err != nil {
			t.Errorf("%s: Ticket(\"OR-1\") returned an error: %v", name, err)
		}
	}
}
