package workload

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"

	"github.com/jbaehova/onthego/internal/config"
)

func SaveReceipt(receipt Receipt) error {
	dir, err := config.ProjectDataRoot(receipt.ProjectID)
	if err != nil {
		return err
	}
	runs := filepath.Join(dir, "runs")
	if err := os.MkdirAll(runs, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(runs, "receipt-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, filepath.Join(runs, receipt.TransferID+".json"))
}

func LoadReceipt(projectID, transferID string) (Receipt, error) {
	dir, err := config.ProjectDataRoot(projectID)
	if err != nil {
		return Receipt{}, err
	}
	data, err := os.ReadFile(filepath.Join(dir, "runs", transferID+".json"))
	if err != nil {
		return Receipt{}, err
	}
	var receipt Receipt
	if err := json.Unmarshal(data, &receipt); err != nil {
		return Receipt{}, err
	}
	if receipt.ProjectID != projectID || receipt.TransferID != transferID {
		return Receipt{}, errors.New("receipt identity mismatch")
	}
	return receipt, nil
}

func LatestReceipt(projectID, workloadName string) (Receipt, error) {
	receipts, err := ListReceipts(projectID)
	if err != nil {
		return Receipt{}, err
	}
	for _, receipt := range receipts {
		if receipt.WorkloadName == workloadName {
			return receipt, nil
		}
	}
	return Receipt{}, os.ErrNotExist
}

func ListReceipts(projectID string) ([]Receipt, error) {
	dir, err := config.ProjectDataRoot(projectID)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(filepath.Join(dir, "runs"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var found []Receipt
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, "runs", entry.Name()))
		if err != nil {
			continue
		}
		var receipt Receipt
		if json.Unmarshal(data, &receipt) == nil && receipt.ProjectID == projectID {
			found = append(found, receipt)
		}
	}
	sort.Slice(found, func(i, j int) bool { return found[i].CreatedAt.After(found[j].CreatedAt) })
	return found, nil
}
