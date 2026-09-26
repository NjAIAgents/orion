// A stable colour per actor id, shared between panel-run.js's log lines and
// panel-detail.js's raw Steps table -- the same hashed-palette approach
// panel-run.js's own ticketColorVar already used for ticket keys, factored
// out once a second file needed the identical logic rather than a second
// copy of the hash function drifting from the first.
//
// Deterministic, not first-come-first-served: a hash of the actor id gives
// every reader the same colour for "implementer" across reloads and across
// pages, with no shared history to consult (the same reasoning
// panel-run.js's own ticketColorVar comment states for ticket keys).
const ACTOR_COLORS = ["--t-orange", "--t-violet", "--t-teal", "--t-rose", "--t-sky"];

export function actorColorVar(actor) {
  if (!actor) return null;
  let h = 0;
  for (let i = 0; i < actor.length; i++) {
    h = (h * 31 + actor.charCodeAt(i)) | 0;
  }
  return ACTOR_COLORS[Math.abs(h) % ACTOR_COLORS.length];
}
