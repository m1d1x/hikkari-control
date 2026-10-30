package entry

import (
	"context"
	"fmt"
)

// ProviderCatalog is a hoster's offer, scoped to one account even when that
// account is not saved or enabled. Reading it never enables purchases.
type ProviderCatalog struct {
	Kind    string      `json:"kind"`
	Regions []BoxRegion `json:"regions"`
	Plans   []BoxPlan   `json:"plans"`
}

func (s *Service) hostingClientFor(ctx context.Context, kind string, credentials map[string]string) (BoxProvider, error) {
	k, ok := s.providerKind(kind)
	if !ok {
		return nil, ErrUnknownProvider
	}
	if k.Role != RoleHosting {
		return nil, fmt.Errorf("%w: у регистратора нет регионов и тарифов машин", ErrRejected)
	}
	if credentials == nil {
		row, exists, err := s.repo.provider(ctx, kind)
		if err != nil {
			return nil, err
		}
		if !exists || row.CredsCT == nil {
			return nil, fmt.Errorf("%w: введи ключ провайдера", ErrRejected)
		}
		credentials, err = openCreds(row.CredsCT)
		if err != nil {
			return nil, err
		}
	}
	creds, err := completeCreds(k, credentials)
	if err != nil {
		return nil, err
	}
	return s.connector.Hosting(kind, creds)
}

func readProviderCatalog(ctx context.Context, kind string, bp BoxProvider) (ProviderCatalog, error) {
	regions, err := bp.Regions(ctx)
	if err != nil {
		return ProviderCatalog{}, err
	}
	plans, err := bp.Plans(ctx)
	if err != nil {
		return ProviderCatalog{}, err
	}
	if regions == nil {
		regions = []BoxRegion{}
	}
	if plans == nil {
		plans = []BoxPlan{}
	}
	return ProviderCatalog{Kind: kind, Regions: regions, Plans: plans}, nil
}

// PreviewProviderCatalog checks draft credentials and reads that account's
// catalog without persisting credentials, defaults, or the enabled state.
// Nil credentials uses this kind's saved key, including a disabled account.
func (s *Service) PreviewProviderCatalog(ctx context.Context, kind string, credentials map[string]string) (ProviderCatalog, error) {
	ctx, cancel := context.WithTimeout(ctx, providerCheckTimeout)
	defer cancel()
	bp, err := s.hostingClientFor(ctx, kind, credentials)
	if err != nil {
		return ProviderCatalog{}, err
	}
	if _, err := bp.Account(ctx); err != nil {
		return ProviderCatalog{}, fmt.Errorf("%w: %v", ErrProviderCheck, err)
	}
	return readProviderCatalog(ctx, kind, bp)
}

func (s *Service) validateHostingDefaults(ctx context.Context, kind string, u ProviderUpdate) error {
	if u.DefaultRegion == "" || u.DefaultPlan == "" {
		return fmt.Errorf("%w: выбери регион и тариф из каталога провайдера", ErrRejected)
	}
	ctx, cancel := context.WithTimeout(ctx, providerCheckTimeout)
	defer cancel()
	bp, err := s.hostingClientFor(ctx, kind, u.Credentials)
	if err != nil {
		return err
	}
	cat, err := readProviderCatalog(ctx, kind, bp)
	if err != nil {
		return err
	}
	for _, region := range cat.Regions {
		if region.ID != u.DefaultRegion {
			continue
		}
		for _, plan := range cat.Plans {
			if plan.ID == u.DefaultPlan && plan.AvailableIn(region.ID) && plan.MonthlyCents > 0 {
				return nil
			}
		}
		return fmt.Errorf("%w: выбранный тариф недоступен в этом регионе — обнови каталог", ErrRejected)
	}
	return fmt.Errorf("%w: выбранного региона нет у провайдера — обнови каталог", ErrRejected)
}
