package query

import (
	"dbviewer/internal/model"
	"dbviewer/internal/sqltok"
)

// Read-only enforcement in the service layer. Statements are tokenized per
// dialect and classified by their token stream (see sqltok.Classify); on top of
// that every query on a read-only connection runs inside a read-only transaction
// the app opens and always rolls back, so a misclassified statement still cannot
// write or change the session.

// ReadOnlyCheck returns "" when stmt (one statement) may run on a read-only
// connection, otherwise the reason it is refused.
func ReadOnlyCheck(driver, stmt string) string {
	rs := sqltok.Split(driver, stmt)
	if len(rs) != 1 {
		if len(rs) == 0 {
			return "empty statement"
		}
		return "multiple statements are not allowed"
	}
	if sqltok.Tokenize(driver, stmt).Unterminated {
		return "unterminated string, identifier or comment"
	}
	return sqltok.Classify(driver, rs[0].Tokens).ReadOnly
}

// ReadOnlyCheckFragment validates raw WHERE/ORDER BY input for read-only connections.
func ReadOnlyCheckFragment(driver, s string) error {
	if why := sqltok.CheckFragment(driver, s); why != "" {
		return model.Errf(model.ErrReadOnly, "not allowed on a read-only connection: %s", why)
	}
	return nil
}
