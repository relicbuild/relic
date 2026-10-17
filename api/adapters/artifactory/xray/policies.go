package xray

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"uuid"

	"relic/api/adapters/artifactory/xraypolicy"

	"relic/api/adapters/artifactory/wire"
	"relic/internal/access/auth"
	"relic/internal/scanning/scan/policy"
)

func (h *Handler) listPolicies(w http.ResponseWriter, r *http.Request) {
	list, err := h.admin.Policies(r.Context())
	if err != nil {
		h.internal(w, r, err)
		return
	}

	wire.WriteJSON(w, http.StatusOK, xraypolicy.Policies(list))
}

func (h *Handler) getPolicy(w http.ResponseWriter, r *http.Request) {
	p, err := h.admin.Policy(r.Context(), r.PathValue("name"))
	if err != nil {
		h.adminError(w, r, err)
		return
	}

	wire.WriteJSON(w, http.StatusOK, xraypolicy.PolicyToXray(p))
}

func (h *Handler) putPolicy(w http.ResponseWriter, r *http.Request) {
	var doc xraypolicy.Policy
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&doc); err != nil {
		writeError(w, http.StatusBadRequest, "invalid policy JSON")
		return
	}

	p := xraypolicy.PolicyFromXray(doc)
	if name := r.PathValue("name"); name != "" {
		p.Name = name
	}

	create := r.Method == http.MethodPost

	if !create {
		stored, err := h.admin.Policy(r.Context(), p.Name)
		if err == nil {
			p = xraypolicy.KeepRelicCriteria(p, stored)
		}
	}

	if err := h.admin.SavePolicy(r.Context(), p, create); err != nil {
		h.adminError(w, r, err)
		return
	}

	if create {
		wire.WriteJSON(
			w,
			http.StatusCreated,
			map[string]string{
				"info": "Policy " + p.Name + " created.",
			},
		)

		return
	}

	wire.WriteJSON(w, http.StatusOK, map[string]string{
		"info": "Policy " + p.Name + " updated.",
	})
}

func (h *Handler) deletePolicy(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := h.admin.DeletePolicy(r.Context(), name); err != nil {
		h.adminError(w, r, err)
		return
	}

	wire.WriteJSON(w, http.StatusOK, map[string]string{
		"info": "Policy " + name + " deleted.",
	})
}

func (h *Handler) listWatches(w http.ResponseWriter, r *http.Request) {
	list, err := h.admin.Watches(r.Context())
	if err != nil {
		h.internal(w, r, err)
		return
	}

	kinds, err := h.policyKinds(r.Context())
	if err != nil {
		h.internal(w, r, err)
		return
	}

	wire.WriteJSON(w, http.StatusOK, xraypolicy.Watches(list, kinds))
}

func (h *Handler) getWatch(w http.ResponseWriter, r *http.Request) {
	wt, err := h.admin.Watch(r.Context(), r.PathValue("name"))
	if err != nil {
		h.adminError(w, r, err)
		return
	}

	kinds, err := h.policyKinds(r.Context())
	if err != nil {
		h.internal(w, r, err)
		return
	}

	wire.WriteJSON(w, http.StatusOK, xraypolicy.WatchToXray(wt, kinds))
}

func (h *Handler) policyKinds(ctx context.Context) (map[string]policy.Kind, error) {
	ps, err := h.admin.Policies(ctx)
	if err != nil {
		return nil, err
	}

	return xraypolicy.Kinds(ps), nil
}

func (h *Handler) putWatch(w http.ResponseWriter, r *http.Request) {
	var doc xraypolicy.Watch
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&doc); err != nil {
		writeError(w, http.StatusBadRequest, "invalid watch JSON")
		return
	}

	wt := xraypolicy.WatchFromXray(doc)
	if name := r.PathValue("name"); name != "" {
		wt.Name = name
	}

	create := r.Method == http.MethodPost

	if err := h.admin.SaveWatch(r.Context(), wt, create); err != nil {
		h.adminError(w, r, err)
		return
	}

	if create {
		wire.WriteJSON(w, http.StatusCreated, map[string]string{
			"info": "Watch created.",
		})

		return
	}

	wire.WriteJSON(w, http.StatusOK, map[string]string{
		"info": "Watch updated.",
	})
}

func (h *Handler) deleteWatch(w http.ResponseWriter, r *http.Request) {
	if err := h.admin.DeleteWatch(r.Context(), r.PathValue("name")); err != nil {
		h.adminError(w, r, err)
		return
	}

	wire.WriteJSON(w, http.StatusOK, map[string]string{
		"info": "Watch deleted.",
	})
}

func (h *Handler) createIgnoreRule(w http.ResponseWriter, r *http.Request) {
	var doc xraypolicy.IgnoreRule
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&doc); err != nil {
		writeError(w, http.StatusBadRequest, "invalid ignore rule JSON")
		return
	}

	native, err := xraypolicy.IgnoreRuleFromXray(doc)
	if err != nil {
		h.adminError(w, r, err)
		return
	}

	rule, err := h.admin.CreateIgnoreRule(r.Context(), native, auth.FromContext(r.Context()).Name())
	if err != nil {
		h.adminError(w, r, err)
		return
	}

	wire.WriteJSON(
		w,
		http.StatusCreated,
		map[string]string{
			"info": "Ignore rule created, id: " + rule.ID,
		},
	)
}

func (h *Handler) listIgnoreRules(w http.ResponseWriter, r *http.Request) {
	list, err := h.admin.IgnoreRules(r.Context(), false)
	if err != nil {
		h.internal(w, r, err)
		return
	}

	wire.WriteJSON(w, http.StatusOK, map[string]any{
		"data":        xraypolicy.IgnoreRules(list),
		"total_count": len(list),
	})
}

func (h *Handler) getIgnoreRule(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "ignore rule not found")
		return
	}

	rule, err := h.admin.IgnoreRule(r.Context(), id)
	if err != nil {
		h.adminError(w, r, err)
		return
	}

	wire.WriteJSON(w, http.StatusOK, xraypolicy.IgnoreRuleToXray(rule))
}

func (h *Handler) deleteIgnoreRule(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "ignore rule not found")
		return
	}

	if err := h.admin.DeleteIgnoreRule(r.Context(), id); err != nil {
		h.adminError(w, r, err)
		return
	}

	wire.WriteJSON(w, http.StatusOK, map[string]string{
		"info": "Ignore rule " + id.String() + " deleted.",
	})
}

func (h *Handler) adminError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, policy.ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, policy.ErrExists):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, policy.ErrInvalid), errors.Is(err, policy.ErrInUse):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		h.internal(w, r, err)
	}
}

func (h *Handler) loadWatches(ctx context.Context) ([]policy.Watch, error) {
	return h.admin.Watches(ctx)
}

func (h *Handler) loadPolicies(ctx context.Context) (map[string]policy.Policy, error) {
	return h.admin.PolicyMap(ctx)
}

func (h *Handler) loadIgnoreRules(ctx context.Context) ([]policy.IgnoreRule, error) {
	return h.admin.IgnoreRules(ctx, true)
}

func (h *Handler) internal(w http.ResponseWriter, r *http.Request, err error) {
	h.log.ErrorContext(r.Context(), "xray api", "error", err)
	writeError(w, http.StatusInternalServerError, "internal error")
}
