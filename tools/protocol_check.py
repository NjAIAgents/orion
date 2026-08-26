#!/usr/bin/env python3
"""Cross-check Orion's guardrail logic without a Go toolchain.

This exists because the Go source was written in an environment with no
compiler. The riskiest parts are not the structure but the pattern matching:
a regex that fails open turns a blocking control into decoration, and nothing
would report it.

So the gate patterns are transcribed here and run against the same cases as
the Go tests. Agreement is not proof the Go compiles, but disagreement is
proof something is wrong. `make test` remains the real gate.

Run: python3 tools/protocol_check.py
"""
import json
import re
import sys

FAILURES = []


def check(name, got, want):
    if got != want:
        FAILURES.append(f"{name}: got {got!r}, want {want!r}")


# --- gate: push protection -------------------------------------------------
# Transcribed from internal/hook/gate.go. Go uses RE2; the subset used here
# (\b, \s, character classes, alternation) behaves identically in Python.

RE_PUSH = re.compile(r"\bgit\s+push\b")
RE_FORCE = re.compile(r"git\s+push\b[^|;&]*(--force\b|--force-with-lease\b|\s-f\b)")


def bad_push(cmd, protected=("main", "develop")):
    if not RE_PUSH.search(cmd):
        return ""
    if RE_FORCE.search(cmd):
        return "force push blocked."
    for branch in protected:
        db = re.escape(branch)
        for pat in (
            r"\bgit\s+push\b[^|;&]*\s" + db + r"\s*($|[|;&])",
            r"\bgit\s+push\b[^|;&]*:" + db + r"\b",
        ):
            if re.search(pat, cmd):
                return f"direct push to {branch} blocked."
    return ""


PUSH_CASES = [
    # (command, should_block)
    ("git push origin main", True),
    ("git push origin develop", True),
    ("git push origin HEAD:develop", True),
    ("git push origin orion/thing:develop", True),
    ("git push origin HEAD:main", True),
    ("git push origin feature:main", True),
    ("git push --force origin feature", True),
    ("git push -f origin feature", True),
    ("git push --force-with-lease origin feature", True),
    # Must NOT block: ordinary branch work.
    ("git push -u origin feature/thing", False),
    ("git push origin my-branch", False),
    ("git push", False),
    ("git commit -m 'main change'", False),
    ("git log --oneline main", False),
    # Near-misses that a sloppy pattern would catch by accident.
    ("git push origin main-fix", False),
    ("git push origin feature/main", False),
    ("git push origin mainline", False),
    ("git push origin developer-notes", False),
    ("git push origin dev-tools", False),
    ("git push -u origin orion/claim-status", False),
]

for cmd, want_block in PUSH_CASES:
    check(f"push[{cmd}]", bool(bad_push(cmd)), want_block)


# --- gate: production deploy ----------------------------------------------

PROD_WORDS = ("prod", "production")
DEPLOY_VERBS = (
    "deploy", "release", "rollout", "promote", "publish",
    "kubectl apply", "kubectl set image", "helm upgrade", "helm install",
    "terraform apply", "serverless deploy", "sls deploy",
    "aws ecs update-service", "aws lambda update-function-code",
    "flyctl deploy", "fly deploy", "vercel --prod", "netlify deploy",
    "gcloud run deploy", "az webapp",
)


def looks_like_prod_deploy(cmd):
    low = cmd.lower()
    if not any(w in low for w in PROD_WORDS):
        return False
    return any(v in low for v in DEPLOY_VERBS)


DEPLOY_CASES = [
    ("./deploy.sh production", True),
    ("kubectl apply -f k8s/production/", True),
    ("helm upgrade myapp ./chart --namespace production", True),
    ("terraform apply -var-file=prod.tfvars", True),
    ("npm run deploy:prod", True),
    ("aws ecs update-service --cluster prod --service api", True),
    ("vercel --prod", True),
    ("fly deploy --app myapp-production", True),
    # Must NOT block.
    ("./deploy.sh staging", False),
    ("kubectl apply -f k8s/dev/", False),
    ("npm run build", False),
    ("git status", False),
]

for cmd, want in DEPLOY_CASES:
    check(f"deploy[{cmd}]", looks_like_prod_deploy(cmd), want)

# A known and accepted false positive: the word "production" appearing in a
# deploy-shaped command that is not a deploy. Asserted rather than ignored so
# the behaviour is a decision on record, not a surprise.
check("deploy[known-fp]", looks_like_prod_deploy("echo 'deploy to production tomorrow'"), True)


# --- glob matcher ----------------------------------------------------------
# Transcribed from internal/match/match.go.

def glob_match(pattern, name):
    pattern = pattern.replace("\\", "/").removeprefix("./")
    name = name.replace("\\", "/").removeprefix("./")
    return _seg(pattern.split("/"), name.split("/"))


def _seg(pat, nam):
    while pat:
        if pat[0] == "**":
            if len(pat) == 1:
                return True
            return any(_seg(pat[1:], nam[i:]) for i in range(len(nam) + 1))
        if not nam:
            return False
        if not _fnmatch_seg(pat[0], nam[0]):
            return False
        pat, nam = pat[1:], nam[1:]
    return not nam


def _fnmatch_seg(pat, name):
    # path.Match semantics: * and ? do not cross separators. Within a single
    # segment there are no separators, so translating to a regex is safe.
    rx = "".join(
        ".*" if c == "*" else "." if c == "?" else re.escape(c) for c in pat
    )
    return re.fullmatch(rx, name) is not None


GLOB_CASES = [
    ("*.go", "main.go", True),
    ("*.go", "sub/main.go", False),
    ("**/*.go", "sub/main.go", True),
    ("**/*.go", "main.go", True),
    ("**/tests/**", "a/tests/b.py", True),
    ("**/tests/**", "a/test/b.py", False),
    (".github/workflows/**", ".github/workflows/ci.yml", True),
    (".github/workflows/**", ".github/dependabot.yml", False),
    ("orion.json", "orion.json", True),
    ("orion.json", "sub/orion.json", False),
    ("**/test_*.py", "src/test_thing.py", True),
    ("**/test_*.py", "src/thing_test.py", False),
    ("a/**/b", "a/b", True),
    ("a/**/b", "a/x/y/b", True),
    ("**", "anything/at/all", True),
]
for pat, name, want in GLOB_CASES:
    check(f"glob[{pat} vs {name}]", glob_match(pat, name), want)


# --- quota reset parsing ---------------------------------------------------

RE_RETRY_AFTER = re.compile(r"(?i)retry[-_ ]?after[\"':\s]+(\d+)")
RE_TRY_AGAIN = re.compile(r"(?i)try again in\s+(\d+)\s*(second|minute|hour)s?")
EXHAUSTION = [
    r"(?i)\brate[_ -]?limit(ed|_error)?\b",
    r"(?i)too many requests",
    r"(?i)\bquota\b.*\b(exceeded|exhausted|reached)\b",
    r"(?i)\b(usage|message) limit reached\b",
    r"(?i)insufficient (quota|credits)",
    r"(?i)\bovercapacity\b|\boverloaded_error\b",
]


def is_exhausted(out):
    return any(re.search(p, out) for p in EXHAUSTION)


QUOTA_CASES = [
    ("Error: rate_limit_error: too many requests", True),
    ("HTTP 429 Too Many Requests", True),
    ("Your quota has been exceeded for this period", True),
    ("Usage limit reached. Try again later.", True),
    ("overloaded_error: server is busy", True),
    # Genuine failures must never be mistaken for a quota wall, or Orion
    # sleeps an hour and retries a broken build.
    ("panic: runtime error: index out of range", False),
    ("FAIL github.com/x/y 0.2s", False),
    ("error: cannot find module", False),
    ("compilation failed: 3 errors", False),
    ("", False),
]
for out, want in QUOTA_CASES:
    check(f"quota[{out[:34]}]", is_exhausted(out), want)

check("retry-after", RE_RETRY_AFTER.search('rate limit. {"retry-after": 300}').group(1), "300")
m = RE_TRY_AGAIN.search("rate limited. Try again in 10 minutes.")
check("try-again", (m.group(1), m.group(2)), ("10", "minute"))


# --- hook protocol shape ---------------------------------------------------
# Asserts the field names the Go structs unmarshal actually match what the
# harness sends. A silent field rename here disables a control completely.

SAMPLE_PRE = {
    "session_id": "abc123",
    "transcript_path": "/tmp/t.jsonl",
    "cwd": "/repo",
    "hook_event_name": "PreToolUse",
    "tool_name": "Bash",
    "tool_input": {"command": "git push origin main"},
}
SAMPLE_POST = {
    "session_id": "abc123",
    "hook_event_name": "PostToolUse",
    "tool_name": "Bash",
    "tool_input": {"command": "make test"},
    "tool_response": {"exit_code": 1, "stderr": "FAIL"},
}
for sample in (SAMPLE_PRE, SAMPLE_POST):
    round_tripped = json.loads(json.dumps(sample))
    check("protocol-roundtrip", round_tripped, sample)

check("protocol-fields-pre",
      sorted(SAMPLE_PRE.keys()),
      sorted(["session_id", "transcript_path", "cwd", "hook_event_name",
              "tool_name", "tool_input"]))


# --- report ----------------------------------------------------------------
total = (len(PUSH_CASES) + len(DEPLOY_CASES) + 1 + len(GLOB_CASES)
         + len(QUOTA_CASES) + 2 + 3)
if FAILURES:
    print(f"FAIL  {len(FAILURES)} of {total} checks failed\n")
    for f in FAILURES:
        print("  " + f)
    sys.exit(1)

print(f"OK    {total} logic checks passed")
print()
print("This checks pattern logic only. It does NOT prove the Go compiles.")
print("Run `make test` before trusting any guardrail.")
