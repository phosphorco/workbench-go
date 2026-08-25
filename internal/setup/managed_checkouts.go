package setup

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"

	"github.com/phosphorco/workbench-go/internal/contract"
)

const managedCheckoutReceiptName = "managed-checkouts.json"

type managedCheckoutReceipt struct {
	Version   int               `json:"version"`
	Resources []receiptResource `json:"resources"`
}

type receiptResource struct {
	Identity           string                 `json:"identity"`
	GitHub             string                 `json:"github"`
	Shape              contract.ResourceShape `json:"shape"`
	CanonicalPath      string                 `json:"canonicalPath"`
	CreatedByWorkbench bool                   `json:"createdByWorkbench"`
}

// ManagedCheckout is durable evidence that setup either created or merely
// adopted a canonical checkout. Prune may consider only CreatedByWorkbench;
// it must still prove every live Git recoverability predicate independently.
type ManagedCheckout struct {
	Identity           string
	GitHub             string
	Shape              contract.ResourceShape
	CanonicalPath      string
	CreatedByWorkbench bool
}

// ReadManagedCheckouts exposes provenance without deletion or migration
// authority. Released legacy state remains readable until setup migrates it.
func ReadManagedCheckouts(root string) ([]ManagedCheckout, error) {
	plan, err := preflightManagedCheckoutMigration(root)
	if err != nil {
		return nil, err
	}
	result := make([]ManagedCheckout, 0, len(plan.receipt.Resources))
	for _, resource := range plan.receipt.Resources {
		result = append(result, ManagedCheckout{
			Identity: resource.Identity, GitHub: resource.GitHub, Shape: resource.Shape,
			CanonicalPath: resource.CanonicalPath, CreatedByWorkbench: resource.CreatedByWorkbench,
		})
	}
	return result, nil
}

type observedReceipt struct {
	path    string
	encoded []byte
	receipt managedCheckoutReceipt
}

type managedCheckoutMigration struct {
	root       string
	receipt    managedCheckoutReceipt
	current    *observedReceipt
	historical *observedReceipt
}

func preflightManagedCheckoutMigration(root string) (managedCheckoutMigration, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return managedCheckoutMigration{}, fmt.Errorf("resolve Workbench root for managed checkouts: %w", err)
	}
	stateDirectory := filepath.Join(root, ".workbench")
	if err := validateStateDirectory(stateDirectory); err != nil {
		return managedCheckoutMigration{}, err
	}
	current, err := observeReceipt(filepath.Join(stateDirectory, managedCheckoutReceiptName), "managed-checkout receipt")
	if err != nil {
		return managedCheckoutMigration{}, err
	}
	historical, err := observeHistoricalV010V030Receipt(stateDirectory)
	if err != nil {
		return managedCheckoutMigration{}, err
	}

	plan := managedCheckoutMigration{root: root, current: current, historical: historical, receipt: managedCheckoutReceipt{Version: 1, Resources: []receiptResource{}}}
	switch {
	case current != nil && historical != nil:
		if !receiptsEqual(current.receipt, historical.receipt) {
			return managedCheckoutMigration{}, fmt.Errorf("managed-checkout receipt migration is ambiguous: current and released 0.1–0.3 state disagree")
		}
		plan.receipt = current.receipt
	case current != nil:
		plan.receipt = current.receipt
	case historical != nil:
		plan.receipt = historical.receipt
	}
	return plan, nil
}

// Apply atomically installs canonical state, proves it can be re-read with the
// exact ownership facts, and only then removes an unchanged historical input.
func (plan managedCheckoutMigration) Apply() error {
	if plan.historical == nil {
		return nil
	}
	if err := plan.reobserveInputs(); err != nil {
		return err
	}
	currentPath := filepath.Join(plan.root, ".workbench", managedCheckoutReceiptName)
	if plan.current == nil {
		encoded, err := encodeManagedCheckoutReceipt(plan.receipt)
		if err != nil {
			return err
		}
		if err := writeDurableReceipt(currentPath, encoded, nil); err != nil {
			return fmt.Errorf("install managed-checkout receipt: %w", err)
		}
	}
	installed, err := observeReceipt(currentPath, "installed managed-checkout receipt")
	if err != nil {
		return err
	}
	if installed == nil || !receiptsEqual(installed.receipt, plan.receipt) {
		return fmt.Errorf("installed managed-checkout receipt did not preserve exact ownership facts")
	}
	observedHistorical, err := observeHistoricalV010V030Receipt(filepath.Dir(currentPath))
	if err != nil {
		return err
	}
	if observedHistorical == nil || !bytes.Equal(observedHistorical.encoded, plan.historical.encoded) || !receiptsEqual(observedHistorical.receipt, plan.receipt) {
		return fmt.Errorf("released 0.1–0.3 managed-checkout state changed during migration; preserved for repair")
	}
	if err := os.Remove(plan.historical.path); err != nil {
		return fmt.Errorf("remove migrated released 0.1–0.3 managed-checkout state: %w", err)
	}
	if err := syncDirectory(filepath.Dir(plan.historical.path)); err != nil {
		return fmt.Errorf("durably remove migrated released 0.1–0.3 managed-checkout state: %w", err)
	}
	return nil
}

func (plan managedCheckoutMigration) reobserveInputs() error {
	if err := validateStateDirectory(filepath.Join(plan.root, ".workbench")); err != nil {
		return err
	}
	current, err := observeReceipt(filepath.Join(plan.root, ".workbench", managedCheckoutReceiptName), "managed-checkout receipt")
	if err != nil {
		return err
	}
	if !sameObservation(current, plan.current) {
		return fmt.Errorf("managed-checkout receipt changed after migration preflight")
	}
	historical, err := observeHistoricalV010V030Receipt(filepath.Join(plan.root, ".workbench"))
	if err != nil {
		return err
	}
	if !sameObservation(historical, plan.historical) {
		return fmt.Errorf("released 0.1–0.3 managed-checkout state changed after migration preflight")
	}
	return nil
}

func sameObservation(left, right *observedReceipt) bool {
	if left == nil || right == nil {
		return left == right
	}
	return left.path == right.path && bytes.Equal(left.encoded, right.encoded) && receiptsEqual(left.receipt, right.receipt)
}

func validateStateDirectory(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("observe Workbench state directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("Workbench state directory is a symlink")
	}
	if !info.IsDir() {
		return fmt.Errorf("Workbench state path is not a directory")
	}
	return nil
}

func observeReceipt(path, label string) (*observedReceipt, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("observe %s: %w", label, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%s is a symlink", label)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", label)
	}
	encoded, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", label, err)
	}
	receipt, err := decodeManagedCheckoutReceipt(encoded, label)
	if err != nil {
		return nil, err
	}
	return &observedReceipt{path: path, encoded: encoded, receipt: receipt}, nil
}

func decodeManagedCheckoutReceipt(encoded []byte, label string) (managedCheckoutReceipt, error) {
	var receipt managedCheckoutReceipt
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&receipt); err != nil {
		return managedCheckoutReceipt{}, fmt.Errorf("decode %s: %w", label, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return managedCheckoutReceipt{}, fmt.Errorf("decode %s: trailing JSON value", label)
		}
		return managedCheckoutReceipt{}, fmt.Errorf("decode %s trailing data: %w", label, err)
	}
	if err := validateManagedCheckoutReceipt(receipt, label); err != nil {
		return managedCheckoutReceipt{}, err
	}
	sortReceiptResources(receipt.Resources)
	return receipt, nil
}

func validateManagedCheckoutReceipt(receipt managedCheckoutReceipt, label string) error {
	if receipt.Version != 1 {
		return fmt.Errorf("unsupported %s version %d", label, receipt.Version)
	}
	identities := make(map[string]struct{}, len(receipt.Resources))
	paths := make(map[string]struct{}, len(receipt.Resources))
	for index, resource := range receipt.Resources {
		declaration := contract.Declaration{Shape: resource.Shape}
		identity, identityErr := declaration.Identity(resource.GitHub)
		canonicalPath, pathErr := declaration.CanonicalPath(resource.GitHub)
		normalizedGitHub, githubErr := contract.NormalizeGitHubRepository(resource.GitHub)
		if identityErr != nil || pathErr != nil || githubErr != nil || normalizedGitHub != resource.GitHub || identity != resource.Identity || canonicalPath != resource.CanonicalPath {
			return fmt.Errorf("%s resource %d disagrees with closed shape identity or placement", label, index)
		}
		if _, duplicate := identities[resource.Identity]; duplicate {
			return fmt.Errorf("%s duplicates identity %q", label, resource.Identity)
		}
		if _, duplicate := paths[resource.CanonicalPath]; duplicate {
			return fmt.Errorf("%s duplicates canonical path %q", label, resource.CanonicalPath)
		}
		identities[resource.Identity] = struct{}{}
		paths[resource.CanonicalPath] = struct{}{}
	}
	return nil
}

func receiptsEqual(left, right managedCheckoutReceipt) bool {
	left = cloneReceipt(left)
	right = cloneReceipt(right)
	sortReceiptResources(left.Resources)
	sortReceiptResources(right.Resources)
	return reflect.DeepEqual(left, right)
}

func cloneReceipt(receipt managedCheckoutReceipt) managedCheckoutReceipt {
	return managedCheckoutReceipt{Version: receipt.Version, Resources: append([]receiptResource(nil), receipt.Resources...)}
}

func sortReceiptResources(resources []receiptResource) {
	sort.Slice(resources, func(i, j int) bool {
		if resources[i].Identity == resources[j].Identity {
			return resources[i].CanonicalPath < resources[j].CanonicalPath
		}
		return resources[i].Identity < resources[j].Identity
	})
}

func encodeManagedCheckoutReceipt(receipt managedCheckoutReceipt) ([]byte, error) {
	receipt = cloneReceipt(receipt)
	sortReceiptResources(receipt.Resources)
	encoded, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode managed-checkout receipt: %w", err)
	}
	return append(encoded, '\n'), nil
}

func writeManagedCheckoutReceipt(root string, resources []Resource, previous managedCheckoutReceipt, created map[string]bool) (bool, error) {
	previousOwnership := make(map[string]bool, len(previous.Resources))
	for _, resource := range previous.Resources {
		previousOwnership[resource.Identity] = resource.CreatedByWorkbench
	}
	receipt := managedCheckoutReceipt{Version: 1, Resources: make([]receiptResource, 0, len(resources)+len(previous.Resources))}
	current := make(map[string]struct{}, len(resources))
	for _, resource := range resources {
		current[resource.Identity] = struct{}{}
		receipt.Resources = append(receipt.Resources, receiptResource{
			Identity: resource.Identity, GitHub: resource.GitHub, Shape: resource.Shape,
			CanonicalPath:      resource.CanonicalPath,
			CreatedByWorkbench: previousOwnership[resource.Identity] || created[resource.Identity],
		})
	}
	// Keep retired entries until an explicit prune action removes its checkout
	// and provenance together. Ordinary setup has neither authority.
	for _, resource := range previous.Resources {
		if _, member := current[resource.Identity]; !member {
			receipt.Resources = append(receipt.Resources, resource)
		}
	}
	encoded, err := encodeManagedCheckoutReceipt(receipt)
	if err != nil {
		return false, err
	}
	path := filepath.Join(root, ".workbench", managedCheckoutReceiptName)
	existing, err := observeReceipt(path, "managed-checkout receipt before update")
	if err != nil {
		return false, err
	}
	if existing != nil && bytes.Equal(existing.encoded, encoded) {
		return false, nil
	}
	if err := writeDurableReceipt(path, encoded, existing); err != nil {
		return false, fmt.Errorf("write managed-checkout receipt: %w", err)
	}
	installed, err := observeReceipt(path, "installed managed-checkout receipt")
	if err != nil {
		return false, err
	}
	if installed == nil || !receiptsEqual(installed.receipt, receipt) {
		return false, fmt.Errorf("installed managed-checkout receipt did not preserve exact ownership facts")
	}
	return true, nil
}

func writeDurableReceipt(path string, contents []byte, expected *observedReceipt) (resultErr error) {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	if err := validateStateDirectory(directory); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".managed-checkouts-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	closed := false
	defer func() {
		if !closed {
			if closeErr := temporary.Close(); resultErr == nil && closeErr != nil {
				resultErr = closeErr
			}
		}
		if removeErr := os.Remove(temporaryPath); resultErr == nil && removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			resultErr = removeErr
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	if _, err := temporary.Write(contents); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	closed = true
	observed, err := observeReceipt(path, "managed-checkout receipt at atomic install")
	if err != nil {
		return err
	}
	if !sameObservation(observed, expected) {
		return fmt.Errorf("managed-checkout receipt changed before atomic install")
	}
	if expected == nil {
		// Link publishes the already-synced inode only if the canonical identity
		// remains absent. Unlike rename, it cannot overwrite a concurrently
		// created receipt whose ownership facts have not been compared.
		if err := os.Link(temporaryPath, path); err != nil {
			return fmt.Errorf("publish new managed-checkout receipt without replacement: %w", err)
		}
		if err := os.Remove(temporaryPath); err != nil {
			return err
		}
	} else if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	return syncDirectory(directory)
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	if err := directory.Sync(); err != nil {
		_ = directory.Close()
		return err
	}
	return directory.Close()
}
