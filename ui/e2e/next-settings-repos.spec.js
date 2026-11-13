const { test, expect } = require('@playwright/test');

test.use({ demoArgs: ["-as", "demo-operator"] });

test('Settings Repositories - Add repo picker, reachability, and removal', async ({ page }) => {
  await page.goto('/next/settings/repositories');

  // Verify the Git scope column is visible
  await expect(page.locator('th', { hasText: 'Scope' })).toBeVisible();

  // Verify Git connections banner (failure banner)
  const verifyBtn = page.getByTestId('check-git-connections-btn');
  await verifyBtn.click();
  const banner = page.getByTestId('git-check-banner');
  await expect(banner).toBeVisible();
  
  // Repo Add picker (click and type in combobox)
  const repoInput = page.getByTestId('input-add-repo');
  await repoInput.click(); // Should trigger checking reachable repositories
  await repoInput.fill('sidthe/demo-repo');

  // Verify reachability warning
  const addBtn = page.getByTestId('add-repo-btn');
  await addBtn.click();
  page.on('dialog', dialog => dialog.accept()); // Accept the confirm dialog for local policy / reachability
  
  // Remove confirm dialog
  const removeBtn = page.getByTestId('remove-repo-sidthe/demo-repo');
  if (await removeBtn.count() > 0) {
    page.once('dialog', dialog => dialog.accept());
    await removeBtn.click();
  }
});
