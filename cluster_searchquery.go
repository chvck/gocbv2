package gocb

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"gopkg.in/couchbase/gocbcore.v7"

	"github.com/opentracing/opentracing-go"
	"gopkg.in/couchbaselabs/jsonx.v1"
)

// SearchResultLocation holds the location of a hit in a list of search results.
type SearchResultLocation struct {
	Position       int    `json:"position,omitempty"`
	Start          int    `json:"start,omitempty"`
	End            int    `json:"end,omitempty"`
	ArrayPositions []uint `json:"array_positions,omitempty"`
}

// SearchResultHit holds a single hit in a list of search results.
type SearchResultHit struct {
	Index       string                                       `json:"index,omitempty"`
	Id          string                                       `json:"id,omitempty"`
	Score       float64                                      `json:"score,omitempty"`
	Explanation map[string]interface{}                       `json:"explanation,omitempty"`
	Locations   map[string]map[string][]SearchResultLocation `json:"locations,omitempty"`
	Fragments   map[string][]string                          `json:"fragments,omitempty"`
	Fields      map[string]string                            `json:"fields,omitempty"`
}

// SearchResultTermFacet holds the results of a term facet in search results.
type SearchResultTermFacet struct {
	Term  string `json:"term,omitempty"`
	Count int    `json:"count,omitempty"`
}

// SearchResultNumericFacet holds the results of a numeric facet in search results.
type SearchResultNumericFacet struct {
	Name  string  `json:"name,omitempty"`
	Min   float64 `json:"min,omitempty"`
	Max   float64 `json:"max,omitempty"`
	Count int     `json:"count,omitempty"`
}

// SearchResultDateFacet holds the results of a date facet in search results.
type SearchResultDateFacet struct {
	Name  string `json:"name,omitempty"`
	Min   string `json:"min,omitempty"`
	Max   string `json:"max,omitempty"`
	Count int    `json:"count,omitempty"`
}

// SearchResultFacet holds the results of a specified facet in search results.
type SearchResultFacet struct {
	Field         string                     `json:"field,omitempty"`
	Total         int                        `json:"total,omitempty"`
	Missing       int                        `json:"missing,omitempty"`
	Other         int                        `json:"other,omitempty"`
	Terms         []SearchResultTermFacet    `json:"terms,omitempty"`
	NumericRanges []SearchResultNumericFacet `json:"numeric_ranges,omitempty"`
	DateRanges    []SearchResultDateFacet    `json:"date_ranges,omitempty"`
}

// SearchResultStatus holds the status information for an executed search query.
type SearchResultStatus struct {
	Total      int `json:"total,omitempty"`
	Failed     int `json:"failed,omitempty"`
	Successful int `json:"successful,omitempty"`
}

// SearchResults allows access to the results of a search query.
type SearchResults interface {
	Status() SearchResultStatus
	Errors() []string
	TotalHits() int
	Hits() []SearchResultHit
	Facets() map[string]SearchResultFacet
	Took() time.Duration
	MaxScore() float64
}

type searchResponse struct {
	Status    SearchResultStatus           `json:"status,omitempty"`
	Errors    []string                     `json:"errors,omitempty"`
	TotalHits int                          `json:"total_hits,omitempty"`
	Hits      []SearchResultHit            `json:"hits,omitempty"`
	Facets    map[string]SearchResultFacet `json:"facets,omitempty"`
	Took      uint                         `json:"took,omitempty"`
	MaxScore  float64                      `json:"max_score,omitempty"`
}

type searchResults struct {
	data *searchResponse
}

// Status is the status information for the results.
func (r searchResults) Status() SearchResultStatus {
	return r.data.Status
}

// Errors are the errors for the results.
func (r searchResults) Errors() []string {
	return r.data.Errors
}

// TotalHits is the actual number of hits before the limit was applied.
func (r searchResults) TotalHits() int {
	return r.data.TotalHits
}

// Hits are the matches for the search query.
func (r searchResults) Hits() []SearchResultHit {
	return r.data.Hits
}

// Facets contains the information relative to the facets requested in the search query.
func (r searchResults) Facets() map[string]SearchResultFacet {
	return r.data.Facets
}

// Took returns the time taken to execute the search.
func (r searchResults) Took() time.Duration {
	return time.Duration(r.data.Took) / time.Nanosecond
}

// MaxScore returns the highest score of all documents for this query.
func (r searchResults) MaxScore() float64 {
	return r.data.MaxScore
}

type searchError struct {
	status int
	// err    viewError TODO
}

func (e *searchError) Error() string {
	return e.err.Error()
}

// Retryable verifies whether or not the error is retryable.
func (e *searchError) Retryable() bool {
	return e.status == 429
}

// SearchQuery performs a n1ql query and returns a list of rows or an error.
func (c *Cluster) SearchQuery(q SearchQuery, opts *SearchQueryOptions) (SearchResults, error) {
	if opts == nil {
		opts = &SearchQueryOptions{}
	}
	ctx := opts.ctx
	if ctx == nil {
		ctx = context.Background()
	}

	var span opentracing.Span
	if opts.parentSpanContext == nil {
		span = opentracing.GlobalTracer().StartSpan("ExecuteSearchQuery",
			opentracing.Tag{Key: "couchbase.service", Value: "cbas"})
	} else {
		span = opentracing.GlobalTracer().StartSpan("ExecuteSearchQuery",
			opentracing.Tag{Key: "couchbase.service", Value: "cbas"}, opentracing.ChildOf(opts.parentSpanContext))
	}
	defer span.Finish()

	provider, err := c.getQueryProvider()
	if err != nil {
		return nil, err
	}

	return c.searchQuery(ctx, span.Context(), q, opts, provider)
}

func (c *Cluster) searchQuery(ctx context.Context, traceCtx opentracing.SpanContext, q SearchQuery, opts *SearchQueryOptions,
	provider queryProvider) (SearchResults, error) {

	qIndexName := q.indexName()
	qBytes, err := json.Marshal(opts.queryData())
	if err != nil {
		return nil, err
	}

	var queryData jsonx.DelayedObject
	err = json.Unmarshal(qBytes, &queryData)
	if err != nil {
		return nil, err
	}

	var ctlData jsonx.DelayedObject
	if queryData.Has("ctl") {
		err = queryData.Get("ctl", &ctlData)
		if err != nil {
			return nil, err
		}
	}

	timeout := c.searchTimeout()
	opTimeout := jsonMillisecondDuration(timeout)
	if ctlData.Has("timeout") {
		err = ctlData.Get("timeout", &opTimeout)
		if err != nil {
			return nil, err
		}
		if opTimeout <= 0 || time.Duration(opTimeout) > timeout {
			opTimeout = jsonMillisecondDuration(timeout)
		}
	}
	err = ctlData.Set("timeout", opTimeout)
	if err != nil {
		return nil, err
	}

	err = queryData.Set("ctl", ctlData)
	if err != nil {
		return nil, err
	}

	err = queryData.Set("query", q.data.Query)

	// Doing this will set the context deadline to whichever is shorter, what is already set or the timeout
	// value
	var cancel context.CancelFunc
	ctx, cancel = context.WithTimeout(ctx, time.Duration(opTimeout))
	defer cancel()

	var retries uint
	var res QueryResults
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
			retries++
			var res SearchResults
			res, err = c.executeSearchQuery(ctx, traceCtx, queryData, qIndexName, provider)
			if err == nil {
				return res, err
			}

			if !isRetryableError(err) || c.sb.SearchRetryBehavior == nil || !c.sb.SearchRetryBehavior.CanRetry(retries) {
				return res, err
			}

			time.Sleep(c.sb.SearchRetryBehavior.NextInterval(retries))
		}
	}
}

func (c *Cluster) executeSearchQuery(ctx context.Context, traceCtx opentracing.SpanContext, query jsonx.DelayedObject,
	qIndexName string, provider queryProvider) (SearchResults, error) {

	qBytes, err := json.Marshal(query)
	if err != nil {
		return nil, err
	}

	req := &gocbcore.HttpRequest{
		Service: gocbcore.FtsService,
		Path:    fmt.Sprintf("/api/index/%s/query", qIndexName),
		Method:  "POST",
		Context: ctx,
		Body:    qBytes,
	}

	dtrace := opentracing.GlobalTracer().StartSpan("dispatch", opentracing.ChildOf(traceCtx))

	resp, err := provider.DoHttpRequest(req)
	if err != nil {
		dtrace.Finish()
		return nil, err
	}

	dtrace.Finish()

	strace := opentracing.GlobalTracer().StartSpan("streaming",
		opentracing.ChildOf(traceCtx))

	ftsResp := searchResponse{}
	errHandled := false
	switch resp.StatusCode {
	case 200:
		jsonDec := json.NewDecoder(resp.Body)
		err = jsonDec.Decode(&ftsResp)
		if err != nil {
			strace.Finish()
			return nil, err
		}
	case 400:
		ftsResp.Status.Total = 1
		ftsResp.Status.Failed = 1
		buf := new(bytes.Buffer)
		_, err := buf.ReadFrom(resp.Body)
		if err != nil {
			strace.Finish()
			return nil, err
		}
		ftsResp.Errors = []string{buf.String()}
		errHandled = true
	case 401:
		ftsResp.Status.Total = 1
		ftsResp.Status.Failed = 1
		ftsResp.Errors = []string{"The requested consistency level could not be satisfied before the timeout was reached"}
		errHandled = true
	}

	err = resp.Body.Close()
	if err != nil {
		logDebugf("Failed to close socket (%s)", err)
	}

	strace.Finish()

	if resp.StatusCode != 200 && !errHandled {
		// return nil, &searchError{
		// 	status: resp.StatusCode,
		// err: viewError{
		// 	Message: "HTTP Error",
		// 	Reason:  fmt.Sprintf("Status code was %d.", resp.StatusCode),
		// }} TODO
	}

	return searchResults{
		data: &ftsResp,
	}, nil
}