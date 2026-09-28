import { expect, test } from '@playwright/test'
import { base, signIn } from './helpers'

// Scenario 1 (US1–US4): create a periodic task from a registered type with the
// generated payload form, see it in the list with its schedule in words, run
// it now and follow the run, stop and start it, then delete it. Needs a full
// platform with at least one module registering a task type; skips without
// operator credentials.
const password = process.env.E2E_OPERATOR_PASSWORD ?? ''
const email = process.env.E2E_OPERATOR_EMAIL ?? 'ops@example.org'

test.describe('scheduler flow', () => {
  test.skip(!password, 'E2E_OPERATOR_PASSWORD not set')

  test('create, run now and follow, stop/start, delete a periodic task', async ({ page }) => {
    await page.goto(base + '/')
    await signIn(page, email, password)
    await page.goto(base + '/scheduler')
    await expect(page.getByTestId('tasks-table')).toBeVisible({ timeout: 15_000 })

    const name = 'e2e ' + Date.now()
    await page.getByTestId('task-new').click()
    const drawer = page.getByTestId('task-drawer')
    await expect(drawer).toBeVisible()
    const typeSelect = drawer.locator('select#task-type')
    const firstType = await typeSelect.locator('option:not([value=""])').first().getAttribute('value')
    test.skip(!firstType, 'no task type registered')
    await typeSelect.selectOption(firstType!)
    await drawer.locator('#task-name').fill(name)
    await drawer.locator('#task-cron').fill('0 3 * * *')
    await expect(drawer.getByTestId('cron-preview').locator('li')).toHaveCount(5, { timeout: 10_000 })
    // Payload: fall back to JSON when the form needs values we cannot guess.
    if (await drawer.getByTestId('schema-form-json').isVisible()) await drawer.getByTestId('schema-form-json').locator('textarea').fill('{}')
    await drawer.getByTestId('task-save').click()
    await expect(drawer).toBeHidden({ timeout: 10_000 })

    const row = page.locator('[data-test^=task-row-]', { hasText: name })
    await expect(row).toBeVisible()
    await expect(row).toContainText('every day at 03:00')

    await row.locator('[data-test^=task-run-]').click()
    const follow = page.getByTestId('execution-drawer')
    await expect(follow).toBeVisible()
    await expect(follow.getByTestId('execution-status')).toBeVisible()
    await follow.getByRole('button', { name: 'Close' }).click()

    await row.locator('[data-test^=task-stop-]').click()
    await expect(row).toContainText('Stopped')
    await row.locator('[data-test^=task-start-]').click()
    await expect(row).toContainText('Enabled')

    await row.locator('[data-test^=task-delete-]').click()
    await page.getByRole('button', { name: 'Delete', exact: true }).click()
    await expect(row).toHaveCount(0)
  })
})
