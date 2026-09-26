package web

// Tickets is the board's whole demand on a tracker (OR-278), and it is
// deliberately one method wide.
//
// WHY A SEAM AT ALL. The board polls the tracker on a background timer for a
// ticket's title and stage (OR-77). A long-running poller needs a credential,
// and the path of least resistance is to keep one where the process can find
// it again after a restart -- on disk, under ORION_HOME, next to the registry.
// Anything that then reads that tree reads the token with it: a backup, a
// dotfiles repo someone syncs, or this package rendering its own state into a
// page. A Jira token carries issue-read and usually issue-write scope across
// every project it can see, so that is not a leak of one board's data.
//
// SO THE CREDENTIAL NEVER ARRIVES HERE. An implementation of Tickets is built
// ALREADY AUTHENTICATED, once, at process start, from the environment or the
// OS keychain (internal/creds is where Orion resolves one). No method below
// takes a credential, returns one, or can be constructed from one, so there is
// no value for this package to hold and therefore none for it to serialize.
// The board holds a Tickets, not a client with a Token field on it.
//
// The negative test that pins this lives at the repository root
// (trackercredential_test.go): it walks the types reachable from Snapshot, and
// the registry and config files that DO get written to ORION_HOME, and fails
// on a credential-shaped field appearing in any of them. The rule is only
// worth stating here because something checks it.
type Tickets interface {
	// Ticket resolves one key. The key is all the caller has -- a card is
	// drawn on a ticket key -- and it is all this takes.
	Ticket(key string) (Ticket, error)
}

// Ticket is the tracker's half of a card: what the log cannot know.
//
// NARROWER THAN THE TRACKER'S OWN RECORD, on purpose. tracker.Issue carries
// labels, links, rank, components and a description, none of which a card
// draws; passing it through would put the tracker's full record inside the
// board's model and make "what does the board hold" a question about another
// package. These three fields are what OR-77 polls for, and a field the page
// does not show is not here yet -- the same rule model.go follows.
type Ticket struct {
	// Key is the tracker identifier ("OR-278"), matching Card.Key.
	Key string
	// Title is the ticket's summary, as the tracker states it. It fills
	// Card.Title, which the event log has no way to supply.
	Title string
	// Stage is the tracker's own status word for the ticket ("In Progress"),
	// by that project's name for it rather than an Orion vocabulary.
	//
	// Distinct from Card.Verb, which is Orion's judgement of how a RUN is
	// going. A ticket sitting in review with no agent on it has a stage and
	// no session, which is exactly the card the board exists to explain.
	Stage string
}
