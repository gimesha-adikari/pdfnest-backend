-- Historical master 296838852195f96ca5590b3466a33ec5ae3e39d9; schema-only synthetic PG18.6 fixture.













CREATE TABLE public.about_page_contents (
    id bigint NOT NULL,
    hero_tag character varying(255) DEFAULT 'About Platen PDF'::character varying NOT NULL,
    hero_title character varying(255) DEFAULT 'Built For Modern PDF Workflows'::character varying NOT NULL,
    hero_description text NOT NULL,
    stats_json text DEFAULT '[]'::text,
    section_title character varying(255) DEFAULT 'What Makes Platen PDF Different'::character varying NOT NULL,
    section_subtitle character varying(255) DEFAULT 'More than a PDF converter. A complete PDF workspace.'::character varying NOT NULL,
    highlights_json text DEFAULT '[]'::text,
    studio_title character varying(255) DEFAULT 'Virtual Document Studio'::character varying NOT NULL,
    studio_description text NOT NULL,
    studio_features_json text DEFAULT '[]'::text,
    canvas_title character varying(255) DEFAULT 'Interactive Canvas'::character varying NOT NULL,
    canvas_description text NOT NULL,
    canvas_features_json text DEFAULT '[]'::text,
    security_title character varying(255) DEFAULT 'Privacy & Security'::character varying NOT NULL,
    security_description text NOT NULL,
    roadmap_title character varying(255) DEFAULT 'Looking Ahead'::character varying NOT NULL,
    roadmap_description text NOT NULL,
    roadmap_json text DEFAULT '[]'::text,
    mission_title character varying(255) DEFAULT 'Our Mission'::character varying NOT NULL,
    mission_description text NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone
);



CREATE SEQUENCE public.about_page_contents_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;



ALTER SEQUENCE public.about_page_contents_id_seq OWNED BY public.about_page_contents.id;



CREATE TABLE public.admin_subscription_mutations (
    id uuid NOT NULL,
    operation_key character varying(128) NOT NULL,
    user_id uuid NOT NULL,
    admin_user_id uuid,
    request_fingerprint character(64) NOT NULL,
    requested_tier character varying(50) NOT NULL,
    requested_status character varying(50) NOT NULL,
    requested_credits bigint NOT NULL,
    requested_days bigint NOT NULL,
    state character varying(20) NOT NULL,
    result_tier character varying(50) NOT NULL,
    result_status character varying(50) NOT NULL,
    result_custom_credits bigint NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone
);



CREATE TABLE public.analyzer_sessions (
    id uuid NOT NULL,
    owner_identity character varying(255) NOT NULL,
    source_type character varying(50) NOT NULL,
    git_url text,
    storage_key text,
    repository_name character varying(255) NOT NULL,
    scope_json text,
    selected_domains_json text,
    current_task_id character varying(100),
    status character varying(50) DEFAULT 'CREATED'::character varying NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone
);



CREATE TABLE public.billing_reservations (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    tool_name character varying(100) NOT NULL,
    pages_count bigint DEFAULT 0 NOT NULL,
    images_count bigint DEFAULT 0 NOT NULL,
    units bigint DEFAULT 0 NOT NULL,
    plan_units bigint DEFAULT 0 NOT NULL,
    credit_units bigint DEFAULT 0 NOT NULL,
    status character varying(20) DEFAULT 'reserved'::character varying NOT NULL,
    task_id character varying(100),
    request_path text,
    expires_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone
);



CREATE TABLE public.contact_categories (
    id uuid NOT NULL,
    name character varying(120) NOT NULL,
    slug character varying(120) NOT NULL,
    type character varying(60) NOT NULL,
    description text,
    color character varying(30),
    sort_order bigint DEFAULT 0,
    is_active boolean DEFAULT true NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone
);



CREATE SEQUENCE public.contact_ticket_sequence
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;



CREATE TABLE public.contact_tickets (
    id uuid NOT NULL,
    ticket_number character varying(30) NOT NULL,
    user_id uuid,
    assigned_to_id uuid,
    name character varying(150),
    email character varying(255) NOT NULL,
    category character varying(50) NOT NULL,
    subject character varying(255) NOT NULL,
    message text NOT NULL,
    status character varying(30) DEFAULT 'open'::character varying,
    priority character varying(30) DEFAULT 'normal'::character varying,
    source character varying(30) DEFAULT 'website'::character varying,
    internal_notes text,
    ip_address character varying(64),
    user_agent text,
    resolved_at timestamp with time zone,
    closed_at timestamp with time zone,
    last_activity_at timestamp with time zone,
    created_at timestamp with time zone,
    updated_at timestamp with time zone,
    email_status text
);



CREATE TABLE public.dynamic_tool_items (
    id bigint NOT NULL,
    title character varying(255) NOT NULL,
    description text NOT NULL,
    href character varying(255) NOT NULL,
    category character varying(50) NOT NULL,
    keywords_json text DEFAULT '[]'::text,
    seo_title character varying(255),
    seo_description text,
    intent text,
    related_json text DEFAULT '[]'::text,
    faq_json text DEFAULT '[]'::text,
    features_json text DEFAULT '[]'::text,
    is_new boolean DEFAULT false,
    accept character varying(255),
    multiple boolean DEFAULT false,
    icon_name character varying(100) DEFAULT 'FileText'::character varying,
    sort_order bigint DEFAULT 0,
    is_active boolean DEFAULT true,
    created_at timestamp with time zone,
    updated_at timestamp with time zone
);



CREATE SEQUENCE public.dynamic_tool_items_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;



ALTER SEQUENCE public.dynamic_tool_items_id_seq OWNED BY public.dynamic_tool_items.id;



CREATE TABLE public.home_page_contents (
    id bigint NOT NULL,
    hero_badge_guest character varying(255) NOT NULL,
    hero_badge_free character varying(255) NOT NULL,
    hero_badge_plus character varying(255) NOT NULL,
    hero_badge_pro character varying(255) NOT NULL,
    hero_welcome_back character varying(255) NOT NULL,
    hero_title_guest character varying(255) NOT NULL,
    hero_title_plus character varying(255) NOT NULL,
    hero_title_pro character varying(255) NOT NULL,
    hero_subtitle_guest text NOT NULL,
    hero_subtitle_guest_bold character varying(255) NOT NULL,
    auth_banner_pro_access text NOT NULL,
    auth_banner_free_usage text NOT NULL,
    auth_banner_free_action character varying(255) NOT NULL,
    feature1_title character varying(255) NOT NULL,
    feature1_description text NOT NULL,
    feature2_title character varying(255) NOT NULL,
    feature2_description text NOT NULL,
    feature3_title character varying(255) NOT NULL,
    feature3_description text NOT NULL,
    search_placeholder character varying(255) NOT NULL,
    search_scope_suffix character varying(255) NOT NULL,
    search_empty_title character varying(255) NOT NULL,
    search_empty_description character varying(255) NOT NULL,
    popular_tool_title character varying(255) NOT NULL,
    popular_tool_description text NOT NULL,
    popular_tool_action character varying(255) NOT NULL,
    category_organize_title character varying(255) NOT NULL,
    category_organize_desc text NOT NULL,
    category_editing_title character varying(255) NOT NULL,
    category_editing_desc text NOT NULL,
    category_convert_title character varying(255) NOT NULL,
    category_convert_desc text NOT NULL,
    category_create_title character varying(255) NOT NULL,
    category_create_desc text NOT NULL,
    category_security_title character varying(255) NOT NULL,
    category_security_desc text NOT NULL,
    category_optimize_title character varying(255) NOT NULL,
    category_optimize_desc text NOT NULL,
    category_studio_title character varying(255) NOT NULL,
    category_studio_desc text NOT NULL,
    updated_at timestamp with time zone
);



CREATE SEQUENCE public.home_page_contents_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;



ALTER SEQUENCE public.home_page_contents_id_seq OWNED BY public.home_page_contents.id;



CREATE TABLE public.ocr_language_usages (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    owner_key character varying(255) NOT NULL,
    language_code character varying(64) NOT NULL,
    language_set character varying(255) DEFAULT ''::character varying NOT NULL,
    manual_selection_count bigint DEFAULT 0 NOT NULL,
    manual_correction_count bigint DEFAULT 0 NOT NULL,
    auto_confirmed_count bigint DEFAULT 0 NOT NULL,
    auto_rejected_count bigint DEFAULT 0 NOT NULL,
    last_used_at timestamp with time zone,
    created_at timestamp with time zone,
    updated_at timestamp with time zone
);



CREATE TABLE public.schema_migrations (
    version character varying(64) NOT NULL,
    applied_at timestamp with time zone NOT NULL
);



CREATE TABLE public.studio_assets (
    id character varying(128) NOT NULL,
    document_id uuid NOT NULL,
    asset_type character varying(32) NOT NULL,
    r2_key character varying(512) NOT NULL,
    byte_size bigint NOT NULL,
    mime_type character varying(64) NOT NULL,
    created_at timestamp with time zone
);



CREATE TABLE public.studio_documents (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    original_file_name character varying(255) NOT NULL,
    file_size bigint NOT NULL,
    initial_page_count bigint NOT NULL,
    created_at timestamp with time zone
);



CREATE TABLE public.studio_editor_states (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    document_id uuid NOT NULL,
    session_id uuid NOT NULL,
    base_version_id uuid NOT NULL,
    extract_job_id uuid NOT NULL,
    layout jsonb NOT NULL,
    created_at timestamp with time zone
);



CREATE TABLE public.studio_exports (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    document_id uuid NOT NULL,
    version_id uuid NOT NULL,
    export_format character varying(16) NOT NULL,
    r2_key character varying(512) NOT NULL,
    byte_size bigint NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone
);



CREATE TABLE public.studio_jobs (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    document_id uuid NOT NULL,
    session_id uuid NOT NULL,
    base_version_id uuid NOT NULL,
    result_version_id uuid,
    editor_state_id uuid,
    worker_job_id character varying(128),
    job_type character varying(64) NOT NULL,
    status character varying(32) NOT NULL,
    progress bigint DEFAULT 0 NOT NULL,
    message text,
    error text,
    error_code character varying(64),
    idempotency_key character varying(128) NOT NULL,
    parameters jsonb NOT NULL,
    result jsonb,
    source_key character varying(512),
    payload_key character varying(512),
    reconciled_at timestamp with time zone,
    created_at timestamp with time zone NOT NULL,
    updated_at timestamp with time zone NOT NULL
);



CREATE TABLE public.studio_operations (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    document_id uuid NOT NULL,
    version_id uuid NOT NULL,
    idempotency_key character varying(128) NOT NULL,
    operation_name character varying(64) NOT NULL,
    parameters jsonb NOT NULL,
    target_page_ids jsonb,
    created_at timestamp with time zone
);



CREATE TABLE public.studio_sessions (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid,
    guest_token_hash character varying(64) NOT NULL,
    document_id uuid NOT NULL,
    active_version_id uuid NOT NULL,
    created_at timestamp with time zone,
    last_accessed_at timestamp with time zone NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    title character varying(255),
    deleted_at timestamp with time zone
);



CREATE TABLE public.studio_snapshots (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    version_id uuid NOT NULL,
    asset_id character varying(128) NOT NULL,
    page_count bigint NOT NULL,
    created_at timestamp with time zone
);



CREATE TABLE public.studio_storage_cleanup_tasks (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    object_key character varying(512) NOT NULL,
    attempt_count bigint DEFAULT 0 NOT NULL,
    next_attempt_at timestamp with time zone NOT NULL,
    lease_expires_at timestamp with time zone,
    last_error character varying(512),
    created_at timestamp with time zone,
    updated_at timestamp with time zone
);



CREATE TABLE public.studio_versions (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    document_id uuid NOT NULL,
    parent_version_id uuid,
    preferred_child_id uuid,
    version_number bigint NOT NULL,
    status character varying(20) DEFAULT 'ready'::character varying NOT NULL,
    operation_type character varying(64) NOT NULL,
    virtual_model jsonb NOT NULL,
    snapshot_id uuid,
    is_materialized boolean DEFAULT false NOT NULL,
    created_at timestamp with time zone NOT NULL
);



CREATE TABLE public.subscribe_page_contents (
    id bigint NOT NULL,
    hero_badge character varying(255) NOT NULL,
    hero_title character varying(255) NOT NULL,
    hero_title_gradient character varying(255) NOT NULL,
    hero_subtitle text NOT NULL,
    premium_section_title character varying(255),
    studio_title character varying(255),
    studio_description text,
    studio_bullet_points text,
    canvas_title character varying(255),
    canvas_description text,
    canvas_bullet_points text,
    speed_title character varying(255),
    speed_description text,
    speed_bullet_points text,
    free_title character varying(255),
    free_price character varying(50),
    free_subtitle character varying(255),
    free_bullet_points text,
    plus_title character varying(255),
    plus_monthly_price character varying(50) NOT NULL,
    plus_yearly_price character varying(50) NOT NULL,
    plus_subtitle character varying(255),
    plus_bullet_points text,
    pro_title character varying(255),
    pro_monthly_price character varying(50) NOT NULL,
    pro_yearly_price character varying(50) NOT NULL,
    pro_subtitle character varying(255),
    pro_bullet_points text,
    trial_text character varying(255),
    security_title character varying(255),
    security_subtitle text,
    security_tags text,
    cta_guest_title text,
    cta_free_title text,
    cta_free_subtitle text,
    cta_plus_title text,
    cta_plus_subtitle text,
    cta_pro_title text,
    cta_pro_subtitle text,
    faqs_json text NOT NULL,
    updated_at timestamp with time zone
);



CREATE SEQUENCE public.subscribe_page_contents_id_seq
    START WITH 1
    INCREMENT BY 1
    NO MINVALUE
    NO MAXVALUE
    CACHE 1;



ALTER SEQUENCE public.subscribe_page_contents_id_seq OWNED BY public.subscribe_page_contents.id;



CREATE TABLE public.subscriptions (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    paddle_customer_id character varying(255),
    paddle_subscription_id character varying(255),
    status character varying(50) NOT NULL,
    tier character varying(50) DEFAULT 'free'::character varying,
    billing_interval character varying(20) DEFAULT 'monthly'::character varying,
    trial_ends_at timestamp with time zone,
    custom_credits bigint DEFAULT 0 NOT NULL,
    update_url text,
    cancel_url text,
    current_period_end timestamp with time zone NOT NULL,
    used_units3h bigint DEFAULT 0 NOT NULL,
    used_units_daily bigint DEFAULT 0 NOT NULL,
    used_units_monthly bigint DEFAULT 0 NOT NULL,
    window3_h_reset_at timestamp with time zone NOT NULL,
    window_daily_reset_at timestamp with time zone NOT NULL,
    window_monthly_reset_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone,
    updated_at timestamp with time zone
);



CREATE TABLE public.transactions (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    user_id uuid NOT NULL,
    subscription_id uuid,
    paddle_transaction_id character varying(255),
    amount numeric(10,2) NOT NULL,
    currency character varying(10) NOT NULL,
    status character varying(50) NOT NULL,
    created_at timestamp with time zone
);



CREATE TABLE public.usage_logs (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    tool_name character varying(100) NOT NULL,
    is_credit boolean DEFAULT false NOT NULL,
    pages_count bigint DEFAULT 0,
    created_at timestamp with time zone
);



CREATE TABLE public.user_settings (
    id uuid NOT NULL,
    user_id uuid NOT NULL,
    email_notifications boolean DEFAULT true NOT NULL,
    product_updates boolean DEFAULT true NOT NULL,
    billing_emails boolean DEFAULT true NOT NULL,
    security_alerts boolean DEFAULT true NOT NULL,
    theme character varying(20) DEFAULT 'system'::character varying,
    language character varying(20) DEFAULT 'en'::character varying,
    created_at timestamp with time zone,
    updated_at timestamp with time zone
);



CREATE TABLE public.users (
    id uuid NOT NULL,
    email character varying(255) NOT NULL,
    password_hash character varying(255),
    google_id character varying(255),
    role character varying(50) DEFAULT 'user'::character varying,
    status character varying(50) DEFAULT 'pending'::character varying,
    email_verified boolean DEFAULT false,
    email_verify_token_hash character varying(255),
    email_verify_expires_at timestamp with time zone,
    session_version bigint DEFAULT 0 NOT NULL,
    password_reset_token_hash character varying(255),
    password_reset_expires_at timestamp with time zone,
    password_reset_requested_at timestamp with time zone,
    created_at timestamp with time zone,
    updated_at timestamp with time zone,
    deleted_at timestamp with time zone
);



CREATE TABLE public.webhook_logs (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    event_id character varying(255) NOT NULL,
    event_type character varying(100) NOT NULL,
    status character varying(50) DEFAULT 'processed'::character varying,
    created_at timestamp with time zone
);



ALTER TABLE ONLY public.about_page_contents ALTER COLUMN id SET DEFAULT nextval('public.about_page_contents_id_seq'::regclass);



ALTER TABLE ONLY public.dynamic_tool_items ALTER COLUMN id SET DEFAULT nextval('public.dynamic_tool_items_id_seq'::regclass);



ALTER TABLE ONLY public.home_page_contents ALTER COLUMN id SET DEFAULT nextval('public.home_page_contents_id_seq'::regclass);



ALTER TABLE ONLY public.subscribe_page_contents ALTER COLUMN id SET DEFAULT nextval('public.subscribe_page_contents_id_seq'::regclass);



ALTER TABLE ONLY public.about_page_contents
    ADD CONSTRAINT about_page_contents_pkey PRIMARY KEY (id);



ALTER TABLE ONLY public.admin_subscription_mutations
    ADD CONSTRAINT admin_subscription_mutations_pkey PRIMARY KEY (id);



ALTER TABLE ONLY public.analyzer_sessions
    ADD CONSTRAINT analyzer_sessions_pkey PRIMARY KEY (id);



ALTER TABLE ONLY public.billing_reservations
    ADD CONSTRAINT billing_reservations_pkey PRIMARY KEY (id);



ALTER TABLE ONLY public.contact_categories
    ADD CONSTRAINT contact_categories_pkey PRIMARY KEY (id);



ALTER TABLE ONLY public.contact_tickets
    ADD CONSTRAINT contact_tickets_pkey PRIMARY KEY (id);



ALTER TABLE ONLY public.dynamic_tool_items
    ADD CONSTRAINT dynamic_tool_items_pkey PRIMARY KEY (id);



ALTER TABLE ONLY public.home_page_contents
    ADD CONSTRAINT home_page_contents_pkey PRIMARY KEY (id);



ALTER TABLE ONLY public.ocr_language_usages
    ADD CONSTRAINT ocr_language_usages_pkey PRIMARY KEY (id);



ALTER TABLE ONLY public.schema_migrations
    ADD CONSTRAINT schema_migrations_pkey PRIMARY KEY (version);



ALTER TABLE ONLY public.studio_assets
    ADD CONSTRAINT studio_assets_pkey PRIMARY KEY (id);



ALTER TABLE ONLY public.studio_documents
    ADD CONSTRAINT studio_documents_pkey PRIMARY KEY (id);



ALTER TABLE ONLY public.studio_editor_states
    ADD CONSTRAINT studio_editor_states_pkey PRIMARY KEY (id);



ALTER TABLE ONLY public.studio_exports
    ADD CONSTRAINT studio_exports_pkey PRIMARY KEY (id);



ALTER TABLE ONLY public.studio_jobs
    ADD CONSTRAINT studio_jobs_pkey PRIMARY KEY (id);



ALTER TABLE ONLY public.studio_operations
    ADD CONSTRAINT studio_operations_pkey PRIMARY KEY (id);



ALTER TABLE ONLY public.studio_sessions
    ADD CONSTRAINT studio_sessions_pkey PRIMARY KEY (id);



ALTER TABLE ONLY public.studio_snapshots
    ADD CONSTRAINT studio_snapshots_pkey PRIMARY KEY (id);



ALTER TABLE ONLY public.studio_storage_cleanup_tasks
    ADD CONSTRAINT studio_storage_cleanup_tasks_pkey PRIMARY KEY (id);



ALTER TABLE ONLY public.studio_versions
    ADD CONSTRAINT studio_versions_pkey PRIMARY KEY (id);



ALTER TABLE ONLY public.subscribe_page_contents
    ADD CONSTRAINT subscribe_page_contents_pkey PRIMARY KEY (id);



ALTER TABLE ONLY public.subscriptions
    ADD CONSTRAINT subscriptions_pkey PRIMARY KEY (id);



ALTER TABLE ONLY public.transactions
    ADD CONSTRAINT transactions_pkey PRIMARY KEY (id);



ALTER TABLE ONLY public.usage_logs
    ADD CONSTRAINT usage_logs_pkey PRIMARY KEY (id);



ALTER TABLE ONLY public.user_settings
    ADD CONSTRAINT user_settings_pkey PRIMARY KEY (id);



ALTER TABLE ONLY public.users
    ADD CONSTRAINT users_pkey PRIMARY KEY (id);



ALTER TABLE ONLY public.webhook_logs
    ADD CONSTRAINT webhook_logs_pkey PRIMARY KEY (id);



CREATE INDEX idx_admin_subscription_mutations_admin_user_id ON public.admin_subscription_mutations USING btree (admin_user_id);



CREATE UNIQUE INDEX idx_admin_subscription_mutations_operation_key ON public.admin_subscription_mutations USING btree (operation_key);



CREATE INDEX idx_admin_subscription_mutations_user_id ON public.admin_subscription_mutations USING btree (user_id);



CREATE INDEX idx_analyzer_sessions_created_at ON public.analyzer_sessions USING btree (created_at);



CREATE INDEX idx_analyzer_sessions_current_task_id ON public.analyzer_sessions USING btree (current_task_id);



CREATE INDEX idx_analyzer_sessions_owner_identity ON public.analyzer_sessions USING btree (owner_identity);



CREATE INDEX idx_billing_reservations_created_at ON public.billing_reservations USING btree (created_at);



CREATE INDEX idx_billing_reservations_expires_at ON public.billing_reservations USING btree (expires_at);



CREATE INDEX idx_billing_reservations_task_id ON public.billing_reservations USING btree (task_id);



CREATE INDEX idx_billing_reservations_user_id ON public.billing_reservations USING btree (user_id);



CREATE UNIQUE INDEX idx_contact_categories_slug ON public.contact_categories USING btree (slug);



CREATE INDEX idx_contact_categories_sort_order ON public.contact_categories USING btree (sort_order);



CREATE INDEX idx_contact_categories_type ON public.contact_categories USING btree (type);



CREATE INDEX idx_contact_tickets_assigned_to_id ON public.contact_tickets USING btree (assigned_to_id);



CREATE INDEX idx_contact_tickets_category ON public.contact_tickets USING btree (category);



CREATE INDEX idx_contact_tickets_created_at ON public.contact_tickets USING btree (created_at);



CREATE INDEX idx_contact_tickets_email ON public.contact_tickets USING btree (email);



CREATE INDEX idx_contact_tickets_last_activity_at ON public.contact_tickets USING btree (last_activity_at);



CREATE INDEX idx_contact_tickets_priority ON public.contact_tickets USING btree (priority);



CREATE INDEX idx_contact_tickets_status ON public.contact_tickets USING btree (status);



CREATE UNIQUE INDEX idx_contact_tickets_ticket_number ON public.contact_tickets USING btree (ticket_number);



CREATE INDEX idx_contact_tickets_user_id ON public.contact_tickets USING btree (user_id);



CREATE UNIQUE INDEX idx_doc_idempotency ON public.studio_operations USING btree (document_id, idempotency_key);



CREATE UNIQUE INDEX idx_dynamic_tool_items_href ON public.dynamic_tool_items USING btree (href);



CREATE UNIQUE INDEX idx_dynamic_tool_items_title ON public.dynamic_tool_items USING btree (title);



CREATE UNIQUE INDEX idx_ocr_language_usage ON public.ocr_language_usages USING btree (owner_key, language_code, language_set);



CREATE INDEX idx_ocr_language_usages_last_used_at ON public.ocr_language_usages USING btree (last_used_at);



CREATE INDEX idx_studio_assets_document_id ON public.studio_assets USING btree (document_id);



CREATE INDEX idx_studio_editor_states_base_version_id ON public.studio_editor_states USING btree (base_version_id);



CREATE INDEX idx_studio_editor_states_document_id ON public.studio_editor_states USING btree (document_id);



CREATE UNIQUE INDEX idx_studio_editor_states_extract_job_id ON public.studio_editor_states USING btree (extract_job_id);



CREATE INDEX idx_studio_editor_states_session_id ON public.studio_editor_states USING btree (session_id);



CREATE INDEX idx_studio_exports_document_id ON public.studio_exports USING btree (document_id);



CREATE INDEX idx_studio_exports_expires_at ON public.studio_exports USING btree (expires_at);



CREATE UNIQUE INDEX idx_studio_job_idempotency ON public.studio_jobs USING btree (document_id, idempotency_key);



CREATE INDEX idx_studio_jobs_base_version_id ON public.studio_jobs USING btree (base_version_id);



CREATE INDEX idx_studio_jobs_created_at ON public.studio_jobs USING btree (created_at);



CREATE INDEX idx_studio_jobs_document_id ON public.studio_jobs USING btree (document_id);



CREATE INDEX idx_studio_jobs_editor_state_id ON public.studio_jobs USING btree (editor_state_id);



CREATE INDEX idx_studio_jobs_result_version_id ON public.studio_jobs USING btree (result_version_id);



CREATE INDEX idx_studio_jobs_session_id ON public.studio_jobs USING btree (session_id);



CREATE INDEX idx_studio_jobs_updated_at ON public.studio_jobs USING btree (updated_at);



CREATE UNIQUE INDEX idx_studio_jobs_worker_job_id ON public.studio_jobs USING btree (worker_job_id);



CREATE UNIQUE INDEX idx_studio_operations_version_id ON public.studio_operations USING btree (version_id);



CREATE INDEX idx_studio_sessions_deleted_at ON public.studio_sessions USING btree (deleted_at);



CREATE INDEX idx_studio_sessions_document_id ON public.studio_sessions USING btree (document_id);



CREATE INDEX idx_studio_sessions_expires_at ON public.studio_sessions USING btree (expires_at);



CREATE INDEX idx_studio_sessions_guest_token_hash ON public.studio_sessions USING btree (guest_token_hash);



CREATE INDEX idx_studio_sessions_last_accessed_at ON public.studio_sessions USING btree (last_accessed_at);



CREATE INDEX idx_studio_sessions_user_id ON public.studio_sessions USING btree (user_id);



CREATE INDEX idx_studio_snapshots_asset_id ON public.studio_snapshots USING btree (asset_id);



CREATE UNIQUE INDEX idx_studio_snapshots_version_id ON public.studio_snapshots USING btree (version_id);



CREATE INDEX idx_studio_storage_cleanup_tasks_lease_expires_at ON public.studio_storage_cleanup_tasks USING btree (lease_expires_at);



CREATE INDEX idx_studio_storage_cleanup_tasks_next_attempt_at ON public.studio_storage_cleanup_tasks USING btree (next_attempt_at);



CREATE UNIQUE INDEX idx_studio_storage_cleanup_tasks_object_key ON public.studio_storage_cleanup_tasks USING btree (object_key);



CREATE INDEX idx_studio_versions_created_at ON public.studio_versions USING btree (created_at);



CREATE INDEX idx_studio_versions_document_id ON public.studio_versions USING btree (document_id);



CREATE INDEX idx_studio_versions_parent_version_id ON public.studio_versions USING btree (parent_version_id);



CREATE INDEX idx_studio_versions_snapshot_id ON public.studio_versions USING btree (snapshot_id);



CREATE UNIQUE INDEX idx_subscriptions_paddle_customer_id ON public.subscriptions USING btree (paddle_customer_id);



CREATE UNIQUE INDEX idx_subscriptions_paddle_subscription_id ON public.subscriptions USING btree (paddle_subscription_id);



CREATE INDEX idx_subscriptions_user_id ON public.subscriptions USING btree (user_id);



CREATE UNIQUE INDEX idx_transactions_paddle_transaction_id ON public.transactions USING btree (paddle_transaction_id);



CREATE INDEX idx_transactions_subscription_id ON public.transactions USING btree (subscription_id);



CREATE INDEX idx_transactions_user_id ON public.transactions USING btree (user_id);



CREATE INDEX idx_usage_logs_created_at ON public.usage_logs USING btree (created_at);



CREATE INDEX idx_usage_logs_user_id ON public.usage_logs USING btree (user_id);



CREATE UNIQUE INDEX idx_user_settings_user_id ON public.user_settings USING btree (user_id);



CREATE INDEX idx_users_deleted_at ON public.users USING btree (deleted_at);



CREATE UNIQUE INDEX idx_users_email ON public.users USING btree (email);



CREATE INDEX idx_users_email_verify_token_hash ON public.users USING btree (email_verify_token_hash);



CREATE UNIQUE INDEX idx_users_google_id ON public.users USING btree (google_id);



CREATE INDEX idx_users_password_reset_token_hash ON public.users USING btree (password_reset_token_hash);



CREATE UNIQUE INDEX idx_webhook_logs_event_id ON public.webhook_logs USING btree (event_id);



ALTER TABLE ONLY public.billing_reservations
    ADD CONSTRAINT fk_billing_reservations_user FOREIGN KEY (user_id) REFERENCES public.users(id) ON UPDATE CASCADE ON DELETE CASCADE;



ALTER TABLE ONLY public.contact_tickets
    ADD CONSTRAINT fk_contact_tickets_assigned_to FOREIGN KEY (assigned_to_id) REFERENCES public.users(id) ON UPDATE CASCADE ON DELETE SET NULL;



ALTER TABLE ONLY public.contact_tickets
    ADD CONSTRAINT fk_contact_tickets_user FOREIGN KEY (user_id) REFERENCES public.users(id) ON UPDATE CASCADE ON DELETE SET NULL;



ALTER TABLE ONLY public.studio_assets
    ADD CONSTRAINT fk_studio_assets_document FOREIGN KEY (document_id) REFERENCES public.studio_documents(id) ON UPDATE CASCADE ON DELETE CASCADE;



ALTER TABLE ONLY public.studio_exports
    ADD CONSTRAINT fk_studio_exports_document FOREIGN KEY (document_id) REFERENCES public.studio_documents(id) ON UPDATE CASCADE ON DELETE CASCADE;



ALTER TABLE ONLY public.studio_exports
    ADD CONSTRAINT fk_studio_exports_version FOREIGN KEY (version_id) REFERENCES public.studio_versions(id) ON UPDATE CASCADE ON DELETE CASCADE;



ALTER TABLE ONLY public.studio_operations
    ADD CONSTRAINT fk_studio_operations_document FOREIGN KEY (document_id) REFERENCES public.studio_documents(id) ON UPDATE CASCADE ON DELETE CASCADE;



ALTER TABLE ONLY public.studio_operations
    ADD CONSTRAINT fk_studio_operations_version FOREIGN KEY (version_id) REFERENCES public.studio_versions(id) ON UPDATE CASCADE ON DELETE CASCADE;



ALTER TABLE ONLY public.studio_sessions
    ADD CONSTRAINT fk_studio_sessions_active_version FOREIGN KEY (active_version_id) REFERENCES public.studio_versions(id) ON UPDATE CASCADE ON DELETE RESTRICT;



ALTER TABLE ONLY public.studio_sessions
    ADD CONSTRAINT fk_studio_sessions_document FOREIGN KEY (document_id) REFERENCES public.studio_documents(id) ON UPDATE CASCADE ON DELETE CASCADE;



ALTER TABLE ONLY public.studio_snapshots
    ADD CONSTRAINT fk_studio_snapshots_asset FOREIGN KEY (asset_id) REFERENCES public.studio_assets(id) ON UPDATE CASCADE ON DELETE RESTRICT;



ALTER TABLE ONLY public.studio_versions
    ADD CONSTRAINT fk_studio_versions_document FOREIGN KEY (document_id) REFERENCES public.studio_documents(id) ON UPDATE CASCADE ON DELETE CASCADE;



ALTER TABLE ONLY public.studio_versions
    ADD CONSTRAINT fk_studio_versions_parent_version FOREIGN KEY (parent_version_id) REFERENCES public.studio_versions(id) ON UPDATE CASCADE ON DELETE SET NULL;



ALTER TABLE ONLY public.studio_versions
    ADD CONSTRAINT fk_studio_versions_preferred_child FOREIGN KEY (preferred_child_id) REFERENCES public.studio_versions(id) ON UPDATE CASCADE ON DELETE SET NULL;



ALTER TABLE ONLY public.studio_versions
    ADD CONSTRAINT fk_studio_versions_snapshot FOREIGN KEY (snapshot_id) REFERENCES public.studio_snapshots(id) ON UPDATE CASCADE ON DELETE SET NULL;



ALTER TABLE ONLY public.subscriptions
    ADD CONSTRAINT fk_subscriptions_user FOREIGN KEY (user_id) REFERENCES public.users(id) ON UPDATE CASCADE ON DELETE CASCADE;



ALTER TABLE ONLY public.transactions
    ADD CONSTRAINT fk_transactions_user FOREIGN KEY (user_id) REFERENCES public.users(id) ON UPDATE CASCADE ON DELETE CASCADE;



ALTER TABLE ONLY public.usage_logs
    ADD CONSTRAINT fk_usage_logs_user FOREIGN KEY (user_id) REFERENCES public.users(id) ON UPDATE CASCADE ON DELETE CASCADE;



ALTER TABLE ONLY public.user_settings
    ADD CONSTRAINT fk_user_settings_user FOREIGN KEY (user_id) REFERENCES public.users(id) ON UPDATE CASCADE ON DELETE CASCADE;




INSERT INTO public.schema_migrations(version,applied_at) VALUES ('20260918_01_admin_subscription_mutation','2000-01-01T00:00:00Z');
