package pg

import "strings"

// summarize extracts db.operation.name and db.collection.name from a simple
// SQL statement. It only handles the shapes the demo services use (one
// statement, one main table) and returns empty strings when unsure.
func summarize(sql string) (operation, collection string) {
	fields := strings.Fields(sql)
	if len(fields) == 0 {
		return "", ""
	}
	operation = strings.ToUpper(fields[0])
	var after string
	switch operation {
	case "SELECT", "DELETE":
		after = "FROM"
	case "INSERT":
		after = "INTO"
	case "UPDATE":
		return operation, tableName(fields, 1)
	case "CREATE":
		after = "TABLE"
	default:
		return operation, ""
	}
	for i := 1; i < len(fields); i++ {
		if strings.EqualFold(fields[i], after) {
			j := i + 1
			if operation == "CREATE" {
				// CREATE TABLE IF NOT EXISTS name
				for j < len(fields) && isKeyword(fields[j], "IF", "NOT", "EXISTS") {
					j++
				}
			}
			return operation, tableName(fields, j)
		}
	}
	return operation, ""
}

func tableName(fields []string, i int) string {
	if i >= len(fields) {
		return ""
	}
	name, _, _ := strings.Cut(fields[i], "(")
	return strings.TrimRight(name, ",;")
}

func isKeyword(s string, kws ...string) bool {
	for _, k := range kws {
		if strings.EqualFold(s, k) {
			return true
		}
	}
	return false
}
