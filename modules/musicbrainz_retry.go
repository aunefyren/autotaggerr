package modules

import (
	"errors"

	"github.com/aunefyren/autotaggerr/logger"
)

// mbTransientRetries is how many *extra* attempts a transiently-failed MusicBrainz
// request gets. One, deliberately.
//
// The failure this absorbs is a single 503 or a dropped connection landing on one
// request in the middle of a run — MusicBrainz is a busy public service and a lone
// blip is its common failure mode, not a sustained outage. One retry catches almost
// all of those. More would not: a second failure a second later is evidence the
// service is actually down, and at that point the useful behaviours are the ones
// already built — [ErrTransient] so a file's correlation is not discarded, and the
// stale cache standing in for the answer.
//
// The cost of getting this wrong is why it is not higher. Every attempt is a
// rate-limited request, so during a real outage each retry doubles the time a run
// takes to fail *and* doubles the load Autotaggerr puts on a service that is already
// struggling. One is the number that helps the blip without turning a bad hour for
// MusicBrainz into a worse one.
const mbTransientRetries = 1

// retryTransient runs a MusicBrainz fetch and repeats it if it failed transiently.
//
// **The spacing is the rate limiter's, not a timer's.** Every fetch passed here
// begins with [RateLimit], which sleeps until a full interval has elapsed since the
// last request — and the attempt that just failed *was* a request, so the retry is
// already spaced by exactly the interval the limiter enforces. A backoff of its own
// would either fight that or duplicate it.
//
// Only [ErrTransient] is retried. [ErrEntityGone] is an answer, not a failure, and
// asking again cannot change it; a parse failure or a 4xx is a request this client
// will keep getting wrong, and quietly repeating it would hide the bug rather than
// surviving it.
//
// Callers place this so it runs *before* their fallbacks: for a release fetch it sits
// inside the in-flight coalescing (so waiters get the retried result rather than each
// retrying in turn) and ahead of the stale-cache substitution, which is the answer of
// last resort and should not pre-empt a retry that would have succeeded.
func retryTransient[T any](what string, fetch func() (T, error)) (T, error) {
	var (
		result T
		err    error
	)
	for attempt := 0; ; attempt++ {
		result, err = fetch()
		if err == nil || attempt >= mbTransientRetries || !errors.Is(err, ErrTransient) {
			return result, err
		}
		logger.Log.Debugf("MusicBrainz %s failed transiently (%s); retrying once", what, err.Error())
	}
}

// retryTransientErr is retryTransient for a fetch that returns only an error, where
// the result is written through a pointer the caller already holds.
func retryTransientErr(what string, fetch func() error) error {
	_, err := retryTransient(what, func() (struct{}, error) {
		return struct{}{}, fetch()
	})
	return err
}
