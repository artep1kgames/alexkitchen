--
-- PostgreSQL database dump
--

\restrict PgDI86nbn7eA4VqlDQZDGaYEL0par9cZ9xlYExwH0cCr5XQ1iNTdtGGe2BvMecp

-- Dumped from database version 17.11
-- Dumped by pg_dump version 17.11

SET statement_timeout = 0;
SET lock_timeout = 0;
SET idle_in_transaction_session_timeout = 0;
SET transaction_timeout = 0;
SET client_encoding = 'UTF8';
SET standard_conforming_strings = on;
SELECT pg_catalog.set_config('search_path', '', false);
SET check_function_bodies = false;
SET xmloption = content;
SET client_min_messages = warning;
SET row_security = off;

SET default_tablespace = '';

SET default_table_access_method = heap;

--
-- Name: cart_reservations; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.cart_reservations (
    id bigint NOT NULL,
    cart_token text NOT NULL,
    product_id text NOT NULL,
    quantity numeric(12,3) NOT NULL,
    status text DEFAULT 'active'::text NOT NULL,
    order_id text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    CONSTRAINT cart_reservations_quantity_check CHECK ((quantity > (0)::numeric)),
    CONSTRAINT cart_reservations_status_check CHECK ((status = ANY (ARRAY['active'::text, 'confirmed'::text, 'converted'::text, 'released'::text, 'expired'::text])))
);


--
-- Name: cart_reservations_id_seq; Type: SEQUENCE; Schema: public; Owner: -
--

CREATE SEQUENCE public.cart_reservations_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;


--
-- Name: cart_reservations_id_seq; Type: SEQUENCE OWNED BY; Schema: public; Owner: -
--

ALTER SEQUENCE public.cart_reservations_id_seq OWNED BY public.cart_reservations.id;


--
-- Name: products; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.products (
    id text NOT NULL,
    section text NOT NULL,
    category text NOT NULL,
    name text NOT NULL,
    price numeric(12,2) NOT NULL,
    price_unit text,
    stock numeric(12,3),
    stock_unit text,
    active boolean DEFAULT true NOT NULL,
    status text,
    low_stock_threshold numeric(12,3),
    description text,
    image text,
    sort integer
);


--
-- Name: yookassa_payments; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.yookassa_payments (
    order_id text NOT NULL,
    idempotence_key text NOT NULL,
    payment_id text,
    status text DEFAULT 'creating'::text NOT NULL,
    amount numeric(10,2) DEFAULT 300.00 NOT NULL,
    confirmation_url text,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    updated_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: cart_reservations id; Type: DEFAULT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.cart_reservations ALTER COLUMN id SET DEFAULT nextval('public.cart_reservations_id_seq'::regclass);


--
-- Name: cart_reservations cart_reservations_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.cart_reservations
    ADD CONSTRAINT cart_reservations_pkey PRIMARY KEY (id);


--
-- Name: products products_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.products
    ADD CONSTRAINT products_pkey PRIMARY KEY (id);


--
-- Name: yookassa_payments yookassa_payments_idempotence_key_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.yookassa_payments
    ADD CONSTRAINT yookassa_payments_idempotence_key_key UNIQUE (idempotence_key);


--
-- Name: yookassa_payments yookassa_payments_payment_id_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.yookassa_payments
    ADD CONSTRAINT yookassa_payments_payment_id_key UNIQUE (payment_id);


--
-- Name: yookassa_payments yookassa_payments_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.yookassa_payments
    ADD CONSTRAINT yookassa_payments_pkey PRIMARY KEY (order_id);


--
-- Name: idx_cart_reservations_active_cart_product; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX idx_cart_reservations_active_cart_product ON public.cart_reservations USING btree (cart_token, product_id) WHERE (status = 'active'::text);


--
-- Name: idx_cart_reservations_product_active; Type: INDEX; Schema: public; Owner: -
--

CREATE INDEX idx_cart_reservations_product_active ON public.cart_reservations USING btree (product_id, expires_at) WHERE (status = 'active'::text);


--
-- Name: cart_reservations cart_reservations_product_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.cart_reservations
    ADD CONSTRAINT cart_reservations_product_id_fkey FOREIGN KEY (product_id) REFERENCES public.products(id);


--
-- PostgreSQL database dump complete
--

\unrestrict PgDI86nbn7eA4VqlDQZDGaYEL0par9cZ9xlYExwH0cCr5XQ1iNTdtGGe2BvMecp

