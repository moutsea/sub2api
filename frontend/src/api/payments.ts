import { apiClient } from './client'

export interface CreateCheckoutSessionRequest {
  amount: number
}

export interface CreateCheckoutSessionResponse {
  order_id: string
  session_id: string
  checkout_url: string
  amount: number
  currency: string
}

export interface PaymentOrder {
  id: string
  amount: number
  currency: string
  payment_method: string
  status: string
  created_at: string
  paid_at: string | null
  checkout_url?: string
  expires_at?: string
}

export interface PaymentOrderQuery {
  page?: number
  page_size?: number
}

export interface PaginatedPaymentOrders {
  items: PaymentOrder[]
  total: number
  page: number
  page_size: number
  pages: number
}

export async function createCheckoutSession(
  payload: CreateCheckoutSessionRequest
): Promise<CreateCheckoutSessionResponse> {
  const { data } = await apiClient.post<CreateCheckoutSessionResponse>('/payments/checkout', payload)
  return data
}

export async function getOrders(
  params: PaymentOrderQuery
): Promise<PaginatedPaymentOrders> {
  const { data } = await apiClient.get<PaginatedPaymentOrders>('/payments', { params })
  return data
}

export const paymentsAPI = {
  createCheckoutSession,
  getOrders
}

export default paymentsAPI
