import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { Badge } from './badge'
import { Button } from './button'

describe('UI primitive variants', () => {
  it('preserves the shared button and badge styling contracts', () => {
    render(
      <>
        <Button variant="outline">Save</Button>
        <Badge variant="secondary">Online</Badge>
      </>,
    )

    expect(screen.getByRole('button', { name: 'Save' })).toHaveClass('border-border')
    expect(screen.getByText('Online')).toHaveClass('bg-secondary')
  })
})
