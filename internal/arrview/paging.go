package arrview

import (
	"database/sql/driver"
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/theatarr/theatarr/internal/decide/format"
	"github.com/theatarr/theatarr/internal/decide/profile"
	"modernc.org/sqlite"
)

// Scalar sort keys keep the resource's title and quality semantics in SQL, before paging.
func init() {
	weights := map[string]map[string]int64{}
	for _, fl := range []format.Flavour{format.Radarr, format.Sonarr} {
		weights[string(fl)] = map[string]int64{}
		for _, d := range profile.DefaultDefinitions(fl).All() {
			weights[string(fl)][strings.ToLower(d.Quality.Name)] = int64(d.Weight)
		}
	}
	sqlite.MustRegisterDeterministicScalarFunction("theatarr_quality_weight", 2, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		flavour, _ := args[0].(string)
		name, _ := args[1].(string)
		return weights[flavour][strings.ToLower(name)], nil
	})
	// HistoryRepository orders the arr's indented JSON columns, whose quality/language values are ids.
	sqlite.MustRegisterDeterministicScalarFunction("theatarr_history_quality", 2, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		flavour, _ := args[0].(string)
		name, _ := args[1].(string)
		return strconv.Itoa(qualityFor(flavour, name).ID) + ",", nil
	})
	sqlite.MustRegisterDeterministicScalarFunction("theatarr_history_languages", 1, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		raw, _ := args[0].(string)
		var names []string
		_ = json.Unmarshal([]byte(raw), &names)
		ids := []int{}
		for _, l := range languagesFrom(Radarr, names) {
			ids = append(ids, l.ID)
		}
		b, _ := json.MarshalIndent(ids, "", "  ")
		return string(b), nil
	})
	sqlite.MustRegisterDeterministicScalarFunction("theatarr_queue_id", 2, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		source, _ := args[0].(int64)
		episode, _ := args[1].(int64)
		return queueEpisodeID(source, episode), nil
	})
	sqlite.MustRegisterDeterministicScalarFunction("theatarr_sort_title", 2, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		sortName, _ := args[0].(string)
		if sortName != "" {
			return sortName, nil
		}
		// SQLite's lower() is ASCII-only; sortTitle uses Go's Unicode folding.
		name, _ := args[1].(string)
		return strings.ToLower(name), nil
	})
}

const titleSortSQL = `theatarr_sort_title(t.sort_name,t.name)`

func (p pagingResource) offset() int64 {
	skip := int64(max(p.Page-1, 0))
	if p.PageSize <= 0 {
		return 0
	}
	if skip > math.MaxInt64/int64(p.PageSize) {
		return math.MaxInt64
	}
	return skip * int64(p.PageSize)
}

// ASP.NET binds paging to Int32. The repository clamps negative offsets; a zero LIMIT is an empty page.
// Reject negative limits (SQLite otherwise interprets them as unlimited) and invalid or out-of-range integers.
func validatePaging(w http.ResponseWriter, r *request) bool {
	for _, key := range []string{"page", "pageSize"} {
		if value, ok := r.query(key); ok {
			n, err := strconv.ParseInt(value, 10, 32)
			if err != nil || key == "pageSize" && n < 0 {
				badRequest(w, key, "Invalid paging value", value)
				return false
			}
		}
	}
	return true
}
