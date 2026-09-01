package httpapi

import (
	"errors"
	"net/http"

	"github.com/hkjang/moyro/server/internal/files"
)

// denyGuestMutation centralises the collaboration boundary for operations
// that could expand a restricted guest's scope. Channel membership itself is
// the allow-list, but guests must not create or discover other spaces, start
// DMs, or add/remove other members.
func (h *handlers) denyGuestMutation(w http.ResponseWriter, r *http.Request, errorID string) bool {
	u, err := h.auth.UserByID(r.Context(), userID(r))
	if err != nil {
		writeError(w, http.StatusUnauthorized, errorID, "active user session required")
		return true
	}
	if !u.IsGuest() {
		return false
	}
	writeError(w, http.StatusForbidden, errorID, "guest access is restricted to invited channels")
	return true
}

func (h *handlers) denyGuestEnumeration(w http.ResponseWriter, r *http.Request, errorID string) bool {
	u, err := h.auth.UserByID(r.Context(), userID(r))
	if err != nil {
		writeError(w, http.StatusUnauthorized, errorID, "active user session required")
		return true
	}
	if !u.IsGuest() {
		return false
	}
	writeError(w, http.StatusForbidden, errorID, "team-wide enumeration is unavailable to guests")
	return true
}

func (h *handlers) guestMayViewUser(w http.ResponseWriter, r *http.Request, targetID, errorID string) bool {
	return h.guestMayViewUserWithStatus(w, r, targetID, errorID, http.StatusForbidden)
}

// guestMayViewNamedUser makes a hidden-by-name/email account indistinguishable
// from a missing account. Returning 403 here would let a restricted guest use
// the lookup endpoint as a directory-existence oracle.
func (h *handlers) guestMayViewNamedUser(w http.ResponseWriter, r *http.Request, targetID, errorID string) bool {
	return h.guestMayViewUserWithStatus(w, r, targetID, errorID, http.StatusNotFound)
}

func (h *handlers) guestMayViewUserWithStatus(w http.ResponseWriter, r *http.Request, targetID, errorID string, hiddenStatus int) bool {
	actor, err := h.auth.UserByID(r.Context(), userID(r))
	if err != nil {
		writeError(w, http.StatusUnauthorized, errorID, "active user session required")
		return false
	}
	if !actor.IsGuest() {
		return true
	}
	allowed, err := h.auth.CanGuestSeeUser(r.Context(), actor.ID, targetID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, errorID, "guest visibility could not be checked")
		return false
	}
	if !allowed {
		if hiddenStatus == http.StatusNotFound {
			writeError(w, hiddenStatus, errorID, "user not found")
		} else {
			writeError(w, hiddenStatus, errorID, "user does not share an invited channel")
		}
		return false
	}
	return true
}

// authorizeOriginalFile applies channel membership first, then the guest's
// explicit original-download grant. Metadata and inline thumbnails/previews
// continue to use authorizeFile so image-bearing collaboration still works.
func (h *handlers) authorizeOriginalFile(r *http.Request, fi *files.FileInfo) error {
	if err := h.authorizeFile(r, fi); err != nil {
		return err
	}
	allowed, err := h.auth.CanDownloadFiles(r.Context(), userID(r))
	if err != nil {
		return err
	}
	if !allowed {
		return errors.New("original file downloads are disabled for this guest")
	}
	return nil
}
