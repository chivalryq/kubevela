/*
Copyright 2021 The KubeVela Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package usecase

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	v1 "k8s.io/api/core/v1"
	errors2 "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8syaml "k8s.io/apimachinery/pkg/runtime/serializer/yaml"
	"sigs.k8s.io/controller-runtime/pkg/client"

	common2 "github.com/oam-dev/kubevela/apis/core.oam.dev/common"
	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
	"github.com/oam-dev/kubevela/apis/types"
	pkgaddon "github.com/oam-dev/kubevela/pkg/addon"
	"github.com/oam-dev/kubevela/pkg/apiserver/clients"
	"github.com/oam-dev/kubevela/pkg/apiserver/datastore"
	"github.com/oam-dev/kubevela/pkg/apiserver/log"
	"github.com/oam-dev/kubevela/pkg/apiserver/model"
	apis "github.com/oam-dev/kubevela/pkg/apiserver/rest/apis/v1"
	restutils "github.com/oam-dev/kubevela/pkg/apiserver/rest/utils"
	"github.com/oam-dev/kubevela/pkg/apiserver/rest/utils/bcode"
	"github.com/oam-dev/kubevela/pkg/oam"
	"github.com/oam-dev/kubevela/pkg/utils/apply"
)

// AddonUsecase addon usecase
type AddonUsecase interface {
	GetAddonRegistry(ctx context.Context, name string) (*model.AddonRegistry, error)
	CreateAddonRegistry(ctx context.Context, req apis.CreateAddonRegistryRequest) (*apis.AddonRegistryMeta, error)
	DeleteAddonRegistry(ctx context.Context, name string) error
	UpdateAddonRegistry(ctx context.Context, name string, req apis.UpdateAddonRegistryRequest) (*apis.AddonRegistryMeta, error)
	ListAddonRegistries(ctx context.Context) ([]*apis.AddonRegistryMeta, error)
	ListAddons(ctx context.Context, registry, query string) ([]*apis.DetailAddonResponse, error)
	StatusAddon(ctx context.Context, name string) (*apis.AddonStatusResponse, error)
	GetAddon(ctx context.Context, name string, registry string) (*apis.DetailAddonResponse, error)
	EnableAddon(ctx context.Context, name string, args apis.EnableAddonRequest) error
	DisableAddon(ctx context.Context, name string) error
	ListEnabledAddon(ctx context.Context) ([]*apis.AddonStatusResponse, error)
	UpdateAddon(ctx context.Context, name string, args apis.EnableAddonRequest) error
}

// AddonImpl2AddonRes convert types.Addon to the type apiserver need
func AddonImpl2AddonRes(impl *types.Addon) (*apis.DetailAddonResponse, error) {
	var defs []*apis.AddonDefinition
	for _, def := range impl.Definitions {
		obj := &unstructured.Unstructured{}
		dec := k8syaml.NewDecodingSerializer(unstructured.UnstructuredJSONScheme)
		_, _, err := dec.Decode([]byte(def.Data), nil, obj)
		if err != nil {
			return nil, fmt.Errorf("convert %s file content to definition fail", def.Name)
		}
		defs = append(defs, &apis.AddonDefinition{
			Name:        obj.GetName(),
			DefType:     obj.GetKind(),
			Description: obj.GetAnnotations()["definition.oam.dev/description"],
		})
	}
	return &apis.DetailAddonResponse{
		AddonMeta:   impl.AddonMeta,
		APISchema:   impl.APISchema,
		UISchema:    impl.UISchema,
		Detail:      impl.Detail,
		Definitions: defs,
	}, nil
}

// NewAddonUsecase returns a addon usecase
func NewAddonUsecase(ds datastore.DataStore) AddonUsecase {
	kubecli, err := clients.GetKubeClient()
	if err != nil {
		panic(err)
	}
	return &addonUsecaseImpl{
		addonRegistryCache: make(map[string]*restutils.MemoryCache),
		addonRegistryDS:    ds,
		kubeClient:         kubecli,
		apply:              apply.NewAPIApplicator(kubecli),
	}
}

type addonUsecaseImpl struct {
	addonRegistryCache map[string]*restutils.MemoryCache
	addonRegistryDS    datastore.DataStore
	kubeClient         client.Client
	apply              apply.Applicator
}

// GetAddon will get addon information
func (u *addonUsecaseImpl) GetAddon(ctx context.Context, name string, registry string) (*apis.DetailAddonResponse, error) {
	var addon *types.Addon
	var err error
	var exist bool

	if registry == "" {
		registries, err := u.ListAddonRegistries(ctx)
		if err != nil {
			return nil, err
		}
		for _, r := range registries {
			if addon, exist = u.tryGetAddonFromCache(r.Name, name); !exist {
				addon, err = SourceOf(r).GetAddon(name, pkgaddon.GetLevelOptions)
			}
			if err != nil && !errors.Is(err, pkgaddon.ErrNotExist) {
				return nil, err
			}
			if addon != nil {
				break
			}
		}
	} else if addon, exist = u.tryGetAddonFromCache(registry, name); !exist {
		addonRegistry, err := u.GetAddonRegistry(ctx, registry)
		if err != nil {
			return nil, err
		}
		addon, err = addonRegistry.Git.GetAddon(name, pkgaddon.GetLevelOptions)
		if err != nil && !errors.Is(err, pkgaddon.ErrNotExist) {
			return nil, err
		}
	}

	if addon == nil {
		return nil, bcode.ErrAddonNotExist
	}
	addon.UISchema = renderDefaultUISchema(addon.APISchema)
	a, err := AddonImpl2AddonRes(addon)
	if err != nil {
		return nil, err
	}
	return a, nil
}

func (u *addonUsecaseImpl) StatusAddon(ctx context.Context, name string) (*apis.AddonStatusResponse, error) {
	var app v1beta1.Application
	err := u.kubeClient.Get(context.Background(), client.ObjectKey{
		Namespace: types.DefaultKubeVelaNS,
		Name:      pkgaddon.Convert2AppName(name),
	}, &app)
	if err != nil {
		if errors2.IsNotFound(err) {
			return &apis.AddonStatusResponse{
				Phase:            apis.AddonPhaseDisabled,
				EnablingProgress: nil,
			}, nil
		}
		return nil, bcode.ErrGetAddonApplication
	}

	res := apis.AddonStatusResponse{
		Name:             name,
		Phase:            convertAppStateToAddonPhase(app.Status.Phase),
		EnablingProgress: nil,
	}
	if res.Phase != apis.AddonPhaseEnabled {
		return &res, nil
	}
	var sec v1.Secret
	err = u.kubeClient.Get(ctx, client.ObjectKey{
		Namespace: types.DefaultKubeVelaNS,
		Name:      pkgaddon.Convert2SecName(name),
	}, &sec)
	if err != nil && !errors2.IsNotFound(err) {
		return nil, bcode.ErrAddonSecretGet
	} else if errors2.IsNotFound(err) {
		res.Args = make(map[string]string, len(sec.Data))
		for k, v := range sec.Data {
			res.Args[k] = string(v)
		}

	}

	return &res, nil
}

func (u *addonUsecaseImpl) ListAddons(ctx context.Context, registry, query string) ([]*apis.DetailAddonResponse, error) {
	var addons []*types.Addon
	var listAddons []*types.Addon
	rs, err := u.ListAddonRegistries(ctx)
	if err != nil {
		return nil, err
	}

	for _, r := range rs {
		if registry != "" && r.Name != registry {
			continue
		}
		if false && u.isRegistryCacheUpToDate(r.Name) {
			listAddons = u.getRegistryCache(r.Name)
		} else {
			listAddons, err = SourceOf(r).ListAddons(pkgaddon.GetLevelOptions)
			if err != nil {
				log.Logger.Errorf("fail to get addons from registry %s, %v", r.Name, err)
				continue
			}
			// if list addons, details will be retrieved later
			go func() {
				addonDetails, err := SourceOf(r).ListAddons(pkgaddon.EnableLevelOptions)
				if err != nil {
					return
				}
				u.putRegistryCache(r.Name, addonDetails)
			}()
		}
		addons = mergeAddons(addons, listAddons)
	}

	for i, a := range addons {
		if a.Invisible {
			addons = append(addons[:i], addons[i+1:]...)
		}
	}

	if query != "" {
		var filtered []*types.Addon
		for i, addon := range addons {
			if strings.Contains(addon.Name, query) || strings.Contains(addon.Description, query) {
				filtered = append(filtered, addons[i])
			}
		}
		addons = filtered
	}
	sort.Slice(addons, func(i, j int) bool {
		return addons[i].Name < addons[j].Name
	})

	for _, addon := range addons {
		// render default ui schema
		addon.UISchema = renderDefaultUISchema(addon.APISchema)
	}

	var addonReses []*apis.DetailAddonResponse
	for _, a := range addons {
		addonRes, err := AddonImpl2AddonRes(a)
		if err != nil {
			log.Logger.Errorf("err while converting AddonImpl to DetailAddonResponse: %v", err)
			continue
		}
		addonReses = append(addonReses, addonRes)
	}
	return addonReses, nil
}

func (u *addonUsecaseImpl) DeleteAddonRegistry(ctx context.Context, name string) error {
	return u.addonRegistryDS.Delete(ctx, &model.AddonRegistry{Name: name})
}

func (u *addonUsecaseImpl) CreateAddonRegistry(ctx context.Context, req apis.CreateAddonRegistryRequest) (*apis.AddonRegistryMeta, error) {
	r := addonRegistryModelFromCreateAddonRegistryRequest(req)

	err := u.addonRegistryDS.Add(ctx, r)
	if err != nil {
		if errors.Is(err, datastore.ErrRecordExist) {
			return nil, bcode.ErrAddonRegistryExist
		}
		return nil, err
	}

	return &apis.AddonRegistryMeta{
		Name: r.Name,
		Git:  r.Git,
		OSS:  r.Oss,
	}, nil
}

func (u *addonUsecaseImpl) GetAddonRegistry(ctx context.Context, name string) (*model.AddonRegistry, error) {
	var r = model.AddonRegistry{
		Name: name,
	}
	err := u.addonRegistryDS.Get(ctx, &r)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

func (u addonUsecaseImpl) UpdateAddonRegistry(ctx context.Context, name string, req apis.UpdateAddonRegistryRequest) (*apis.AddonRegistryMeta, error) {
	var r = model.AddonRegistry{
		Name: name,
	}
	err := u.addonRegistryDS.Get(ctx, &r)
	if err != nil {
		return nil, bcode.ErrAddonRegistryNotExist
	}
	r.Git = req.Git
	r.Oss = req.Oss
	err = u.addonRegistryDS.Put(ctx, &r)
	if err != nil {
		return nil, err
	}

	return &apis.AddonRegistryMeta{
		Name: r.Name,
		Git:  r.Git,
		OSS:  r.Oss,
	}, nil
}

func (u *addonUsecaseImpl) ListAddonRegistries(ctx context.Context) ([]*apis.AddonRegistryMeta, error) {
	var r = model.AddonRegistry{}

	var list []*apis.AddonRegistryMeta
	entities, err := u.addonRegistryDS.List(ctx, &r, &datastore.ListOptions{})
	if err != nil {
		return nil, err
	}
	for _, entity := range entities {
		list = append(list, ConvertAddonRegistryModel2AddonRegistryMeta(entity.(*model.AddonRegistry)))
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].Name < list[j].Name
	})
	return list, nil
}

func (u *addonUsecaseImpl) tryGetAddonFromCache(registry, addonName string) (*types.Addon, bool) {
	if u.isRegistryCacheUpToDate(registry) {
		addons := u.getRegistryCache(registry)
		for _, a := range addons {
			if a.Name == addonName {
				return a, true
			}
		}
	}
	return nil, false
}

func (u *addonUsecaseImpl) EnableAddon(ctx context.Context, name string, args apis.EnableAddonRequest) error {
	var addon *types.Addon
	var err error
	registries, err := u.ListAddonRegistries(ctx)
	if err != nil {
		return err
	}
	for _, r := range registries {
		var exist bool
		if addon, exist = u.tryGetAddonFromCache(r.Name, name); !exist {
			addon, err = SourceOf(r).GetAddon(name, pkgaddon.EnableLevelOptions)
		}
		if err != nil && !errors.Is(err, pkgaddon.ErrNotExist) {
			return bcode.WrapGithubRateLimitErr(err)
		}
		if addon == nil {
			continue
		}

		err = pkgaddon.EnableAddon(ctx, addon, u.kubeClient, u.apply, SourceOf(r), args.Args)
		if err != nil {
			log.Logger.Errorf("err when enable addon: %v", err)
			return bcode.ErrAddonApply
		}
		return nil
	}
	return bcode.ErrAddonNotExist
}

func (u *addonUsecaseImpl) getRegistryCache(name string) []*types.Addon {
	return u.addonRegistryCache[name].GetData().([]*types.Addon)
}

func (u *addonUsecaseImpl) putRegistryCache(name string, addons []*types.Addon) {
	u.addonRegistryCache[name] = restutils.NewMemoryCache(addons, time.Minute*10)
}

func (u *addonUsecaseImpl) isRegistryCacheUpToDate(name string) bool {
	d, ok := u.addonRegistryCache[name]
	if !ok {
		return false
	}
	return !d.IsExpired()
}

func (u *addonUsecaseImpl) DisableAddon(ctx context.Context, name string) error {
	app := &v1beta1.Application{
		TypeMeta: metav1.TypeMeta{APIVersion: "core.oam.dev/v1beta1", Kind: "Application"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      pkgaddon.Convert2AppName(name),
			Namespace: types.DefaultKubeVelaNS,
		},
	}
	err := u.kubeClient.Delete(ctx, app)
	if err != nil {
		log.Logger.Errorf("delete application fail: %s", err.Error())
		return err
	}
	return nil
}

func (u *addonUsecaseImpl) ListEnabledAddon(ctx context.Context) ([]*apis.AddonStatusResponse, error) {
	apps := &v1beta1.ApplicationList{}
	if err := u.kubeClient.List(ctx, apps, client.InNamespace(types.DefaultKubeVelaNS), client.HasLabels{oam.LabelAddonName}); err != nil {
		return nil, err
	}
	var response []*apis.AddonStatusResponse
	for _, application := range apps.Items {
		if addonName := application.Labels[oam.LabelAddonName]; addonName != "" {
			if application.Status.Phase != common2.ApplicationRunning {
				continue
			}
			response = append(response, &apis.AddonStatusResponse{
				Name:  addonName,
				Phase: convertAppStateToAddonPhase(application.Status.Phase),
			})
		}
	}
	return response, nil
}

func (u *addonUsecaseImpl) UpdateAddon(ctx context.Context, name string, args apis.EnableAddonRequest) error {

	var app v1beta1.Application
	// check addon application whether exist
	err := u.kubeClient.Get(context.Background(), client.ObjectKey{
		Namespace: types.DefaultKubeVelaNS,
		Name:      pkgaddon.Convert2AppName(name),
	}, &app)
	if err != nil {
		return err
	}

	var addon *types.Addon
	registries, err := u.ListAddonRegistries(ctx)
	if err != nil {
		return err
	}

	for _, r := range registries {
		var exist bool
		if addon, exist = u.tryGetAddonFromCache(r.Name, name); !exist {
			addon, err = SourceOf(r).GetAddon(name, pkgaddon.EnableLevelOptions)
		}
		if err != nil && !errors.Is(err, pkgaddon.ErrNotExist) {
			return bcode.WrapGithubRateLimitErr(err)
		}
		if addon == nil {
			continue
		}

		err = pkgaddon.EnableAddon(ctx, addon, u.kubeClient, u.apply, r.Git, args.Args)
		if err != nil {
			log.Logger.Errorf("err when enable addon: %v", err)
			return bcode.ErrAddonApply
		}
		return nil
	}
	return bcode.ErrAddonNotExist
}

func addonRegistryModelFromCreateAddonRegistryRequest(req apis.CreateAddonRegistryRequest) *model.AddonRegistry {
	return &model.AddonRegistry{
		Name: req.Name,
		Git:  req.Git,
		Oss:  req.Oss,
	}
}

func mergeAddons(a1, a2 []*types.Addon) []*types.Addon {
	for _, item := range a2 {
		if hasAddon(a1, item.Name) {
			continue
		}
		a1 = append(a1, item)
	}
	return a1
}

func hasAddon(addons []*types.Addon, name string) bool {
	for _, addon := range addons {
		if addon.Name == name {
			return true
		}
	}
	return false
}

// ConvertAddonRegistryModel2AddonRegistryMeta will convert from model to AddonRegistryMeta
func ConvertAddonRegistryModel2AddonRegistryMeta(r *model.AddonRegistry) *apis.AddonRegistryMeta {
	return &apis.AddonRegistryMeta{
		Name: r.Name,
		Git:  r.Git,
		OSS:  r.Oss,
	}
}

func convertAppStateToAddonPhase(state common2.ApplicationPhase) apis.AddonPhase {
	switch state {
	case common2.ApplicationRunning:
		return apis.AddonPhaseEnabled
	default:
		return apis.AddonPhaseEnabling
	}
}

// SourceOf returns actual Source in registry meta
func SourceOf(meta *apis.AddonRegistryMeta) pkgaddon.Source {
	if meta.OSS != nil {
		return meta.OSS
	}
	return meta.Git
}
