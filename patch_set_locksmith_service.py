path = "internal/core/users/service.go"
with open(path, encoding="utf-8") as f:
    c = f.read()

old = '''func (s *Service) Deactivate(ctx context.Context, id string) error {
	return s.repo.Deactivate(ctx, id)
}'''

new = '''func (s *Service) Deactivate(ctx context.Context, id string) error {
	return s.repo.Deactivate(ctx, id)
}

func (s *Service) SetLocksmithSlot(ctx context.Context, slot int, userID string) error {
	return s.repo.SetLocksmithSlot(ctx, slot, userID)
}'''

if old not in c:
    raise SystemExit("FEHLER: Service.Deactivate nicht gefunden")
c = c.replace(old, new, 1)

with open(path, "w", encoding="utf-8") as f:
    f.write(c)
print("OK: Service-Wrapper ergaenzt.")


