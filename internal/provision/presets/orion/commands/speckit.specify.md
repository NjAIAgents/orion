## Orion runs this headless — these rules override any conflicting instruction below

This command is being run by Orion inside a supervised, non-interactive stage. There is
no user at the prompt. Orion's discovery gate reads the specification you write and
stops the chain while anything in it is still undecided; a human then answers in the
file. That gate is the clarification step. So, wherever the instructions below say
otherwise:

1. **Never make an informed guess.** Every uncertainty about scope, users, data,
   security, behaviour or constraints becomes a `[NEEDS CLARIFICATION: <specific
   question>]` marker in the specification where the decision belongs. There is **no
   limit** on the number of markers: ignore "LIMIT: Maximum 3", ignore "make informed
   guesses for the rest", and do not rank markers to drop any. A guessed answer is
   indistinguishable from a decision by the time the plan reads it.

2. **Never ask the user.** Under "Handle Validation Results", if `[NEEDS CLARIFICATION]`
   markers remain, do **not** present questions, do **not** build option tables, and do
   **not** wait for a response — skip those steps entirely. Leave the markers in place,
   leave the "No [NEEDS CLARIFICATION] markers remain" checklist item unchecked, and
   finish. Orion reports the markers and a person answers them in the file.

3. **Also list every marker's question as a bullet under a `## Open questions`
   section** of the specification (the template has one). Write `- None` there only
   when the specification contains no marker at all. This is the section Orion's gate
   parses by heading; the markers and the bullets must agree.

4. Do not run or dispatch extension hooks; Orion registers none.

Everything else about the specification — user stories with priorities, functional
requirements, success criteria, key entities, the quality checklist — applies unchanged.

{CORE_TEMPLATE}
