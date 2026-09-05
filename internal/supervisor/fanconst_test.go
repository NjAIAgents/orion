package supervisor

// fanResultJSON is one child's closing result object.
//
// Untagged, unlike most of the fan tests: a JSON string is not POSIX. It sat
// in fan_test.go until that file became !windows -- it uses fakeClaudeTree,
// which writes a shell script -- and three untagged files that need this
// constant stopped compiling on Windows (OR-340).
const fanResultJSON = `{"type":"result","session_id":"s","result":"done",` +
	`"total_cost_usd":0.01,"is_error":false}`
