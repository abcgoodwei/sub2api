export interface CountryFilter {
  mode: 'off' | 'exclude' | 'include'
  codes: string[]
  allow_unknown: boolean
}
export interface CountryNode {
  bound_account_id?: number
  bound_exit_ip?: string
  bound_until?: string
  display_name?: string
  name: string
  state: string
  country_code?: string
  country_checked_at?: string
  country_error?: string
  country_blocked?: boolean
}
