package transfer

import (
	"context"
	"net/http"
	"strings"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"

	relicv1 "relic/api/gen/relic/v1"
	"relic/api/gen/relic/v1/relicv1connect"
)

type relic struct {
	url          string
	client       *http.Client
	repositories relicv1connect.RepositoryServiceClient
	security     relicv1connect.SecurityServiceClient
	artifacts    relicv1connect.ArtifactServiceClient
}

func newRelic(s Server) relic {
	base := strings.TrimSuffix(s.URL, "/")
	api := base + "/api"

	rpc := connect.NewClient(
		connecthttp.NewTransport(
			s.Client,
			api,
			connecthttp.WithReadMaxBytes(0),
		),
	)

	return relic{
		url:          base,
		client:       s.Client,
		repositories: relicv1connect.NewRepositoryServiceClient(rpc),
		security:     relicv1connect.NewSecurityServiceClient(rpc),
		artifacts:    relicv1connect.NewArtifactServiceClient(rpc),
	}
}

func (r relic) files(target string) string {
	return r.url + "/api/v1/files/" + target
}

func found(err error) (bool, error) {
	switch {
	case err == nil:
		return true, nil
	case connect.CodeOf(err) == connect.CodeNotFound:
		return false, nil
	default:
		return false, err
	}
}

func (im *importer) decide(what, name string, exists bool, counts *Counts) bool {
	switch {
	case exists && !im.opts.Update:
		counts.Skipped++

		return false
	case exists:
		counts.Updated++
	default:
		counts.Created++
	}

	if im.opts.DryRun {
		im.progress("would copy %s %s", what, name)

		return false
	}

	return true
}

type groupDoc struct {
	Name            string `json:"name"`
	Description     string `json:"description"`
	AutoJoin        bool   `json:"autoJoin"`
	AdminPrivileges bool   `json:"adminPrivileges"`
}

func (im *importer) putGroup(ctx context.Context, doc groupDoc) error {
	existing, err := im.relic.security.GetGroup(ctx, &relicv1.GetGroupRequest{
		Name: doc.Name,
	})

	exists, err := found(err)
	if err != nil {
		return err
	}

	if !im.decide("group", doc.Name, exists, &im.report.Groups) {
		return nil
	}

	g := &relicv1.Group{
		Name:               doc.Name,
		Description:        doc.Description,
		DefaultForNewUsers: doc.AutoJoin,
		Admin:              doc.AdminPrivileges,
	}
	if exists {
		g.Members = existing.GetGroup().GetMembers()
	}

	if _, err := im.relic.security.PutGroup(ctx, &relicv1.PutGroupRequest{
		Group: g,
	}); err != nil {
		return err
	}

	im.progress("copied group %s", doc.Name)

	return nil
}

func (im *importer) putRepository(ctx context.Context, r *relicv1.Repository) error {
	_, err := im.relic.repositories.GetRepository(ctx, &relicv1.GetRepositoryRequest{
		Name: r.GetName(),
	})

	exists, err := found(err)
	if err != nil {
		return err
	}

	if !im.decide("repository", r.GetName(), exists, &im.report.Repositories) {
		return nil
	}

	if exists {
		_, err = im.relic.repositories.UpdateRepository(ctx, &relicv1.UpdateRepositoryRequest{
			Repository: r,
		})
	} else {
		_, err = im.relic.repositories.CreateRepository(ctx, &relicv1.CreateRepositoryRequest{
			Repository: r,
		})
	}

	if err != nil {
		return err
	}

	im.progress("copied repository %s", r.GetName())

	return nil
}

func (im *importer) putPermission(ctx context.Context, p *relicv1.Permission) error {
	_, err := im.relic.security.GetPermission(ctx, &relicv1.GetPermissionRequest{
		Name: p.GetName(),
	})

	exists, err := found(err)
	if err != nil {
		return err
	}

	if !im.decide("permission", p.GetName(), exists, &im.report.Permissions) {
		return nil
	}

	if _, err := im.relic.security.PutPermission(ctx, &relicv1.PutPermissionRequest{
		Permission: p,
	}); err != nil {
		return err
	}

	im.progress("copied permission %s", p.GetName())

	return nil
}

func (im *importer) stored(ctx context.Context, f file) bool {
	if f.SHA256 == "" {
		return false
	}

	resp, err := im.relic.artifacts.StatItem(ctx, &relicv1.StatItemRequest{
		Repository: f.Repo,
		Path:       f.fullPath(),
	})

	return err == nil && strings.EqualFold(resp.GetItem().GetChecksums().GetSha256(), f.SHA256)
}

func (im *importer) removeFile(ctx context.Context, f file) error {
	_, err := im.relic.artifacts.DeleteItem(ctx, &relicv1.DeleteItemRequest{
		Repository: f.Repo,
		Path:       f.fullPath(),
	})

	return err
}

func (im *importer) setProperties(ctx context.Context, f file, values map[string][]string) error {
	if len(values) == 0 {
		return nil
	}

	props := make(map[string]*relicv1.PropertyValues, len(values))
	for k, vs := range values {
		props[k] = &relicv1.PropertyValues{
			Values: vs,
		}
	}

	_, err := im.relic.artifacts.SetProperties(ctx, &relicv1.SetPropertiesRequest{
		Repository: f.Repo,
		Path:       f.fullPath(),
		Properties: props,
	})

	return err
}
