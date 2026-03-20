-- +goose Up
CREATE TABLE custom_domains (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    vm_id INTEGER NOT NULL REFERENCES vms(id) ON DELETE CASCADE,
    domain TEXT NOT NULL UNIQUE,
    verified BOOLEAN NOT NULL DEFAULT FALSE,
    verification_token TEXT,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    verified_at DATETIME
);

CREATE INDEX idx_custom_domains_vm_id ON custom_domains(vm_id);
CREATE INDEX idx_custom_domains_domain ON custom_domains(domain);

-- +goose Down
DROP TABLE IF EXISTS custom_domains;
