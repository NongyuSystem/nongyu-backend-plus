package pgx

// 测试用 DDL 内嵌副本：与 migrations/ 下的迁移保持同构（原 testschema_ddl.go）。
//
// 为什么不直接读 migrations 文件：测试二进制的工作目录不固定，
// 读文件容易因路径失败而把「测试没跑」伪装成「测试通过」。
// 这里内嵌一份，并在测试里断言它与迁移文件同构。
//
// 只含 DDL 字面量。PGIdentityStore 上的 ExecDDL / ExecDDLInSchema / DeleteUser
// 因为 Go 不允许为其它包类型定义方法，留在 internal/platform/pg_ddl_helpers.go。
//

// identityDDLStatements 返回测试 schema 需要的全部建表语句（幂等）。
// 与 migrations/ 下的迁移保持一致：0001 审计表 + 0002 身份域四表。
func identityDDLStatements() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS audit_log (
			id          bigserial   PRIMARY KEY,
			request_id  text        NOT NULL,
			user_id     text        NOT NULL DEFAULT '',
			action      text        NOT NULL,
			target      text        NOT NULL DEFAULT '',
			result      text        NOT NULL DEFAULT '',
			occurred_at timestamptz NOT NULL,
			created_at  timestamptz NOT NULL DEFAULT now()
		)`,
		`CREATE TABLE IF NOT EXISTS users (
			id          bigserial   PRIMARY KEY,
			student_no  char(9)     NOT NULL UNIQUE,
			role        smallint    NOT NULL DEFAULT 0 CHECK (role IN (0, 1, 2)),
			status      smallint    NOT NULL DEFAULT 1 CHECK (status IN (0, 1)),
			created_at  timestamptz NOT NULL DEFAULT now(),
			updated_at  timestamptz NOT NULL DEFAULT now(),
			-- 登录审计（issue #29）：迁移用 ALTER ADD 追加，列顺序在 updated_at 之后
			last_login_ip     text NOT NULL DEFAULT '',
			last_login_device text NOT NULL DEFAULT '',
			last_login_at     timestamptz
		)`,
		`CREATE TABLE IF NOT EXISTS user_profiles (
			user_id         bigint      PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
			name            text        NOT NULL DEFAULT '',
			id_card_sealed  bytea,
			id_card_suffix4 char(4),
			college         text        NOT NULL DEFAULT '',
			major           text        NOT NULL DEFAULT '',
			class_name      text        NOT NULL DEFAULT '',
			grade           text        NOT NULL DEFAULT '',
			qq              text        NOT NULL DEFAULT '',
			avatar_url      text        NOT NULL DEFAULT '',
			updated_at      timestamptz NOT NULL DEFAULT now(),
			-- 教务档案页字段（issue #29）：迁移用 ALTER ADD 追加，列顺序在 updated_at 之后
			exam_no           text NOT NULL DEFAULT '',
			gender            text NOT NULL DEFAULT '',
			study_duration    text NOT NULL DEFAULT '',
			education_level   text NOT NULL DEFAULT '',
			new_major         text NOT NULL DEFAULT '',
			new_class_name    text NOT NULL DEFAULT '',
			enrollment_status text NOT NULL DEFAULT '',
			enrollment_date   text NOT NULL DEFAULT '',
			nation            text NOT NULL DEFAULT '',
			political_status  text NOT NULL DEFAULT '',
			home_address      text NOT NULL DEFAULT '',
			train_station     text NOT NULL DEFAULT '',
			personal_phone    text NOT NULL DEFAULT '',
			home_phone        text NOT NULL DEFAULT '',
			specialty         text NOT NULL DEFAULT '',
			-- 农屿扩展（本人可改）
			nickname          text NOT NULL DEFAULT '',
			email             text NOT NULL DEFAULT '',
			-- ywtb 字段
			birthday          text NOT NULL DEFAULT '',
			avatar_source_url text NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS login_methods (
			id          bigserial   PRIMARY KEY,
			user_id     bigint      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
			kind        text        NOT NULL,
			identifier  text        NOT NULL DEFAULT '',
			secret_ref  bytea,
			bound_at    timestamptz NOT NULL DEFAULT now(),
			UNIQUE (kind, identifier),
			UNIQUE (user_id, kind)
		)`,
		`CREATE TABLE IF NOT EXISTS sessions (
			id          text        PRIMARY KEY,
			user_id     bigint      NOT NULL REFERENCES users (id) ON DELETE CASCADE,
			scope       text        NOT NULL CHECK (scope IN ('app', 'admin')),
			device_id   text        NOT NULL DEFAULT '',
			expires_at  timestamptz NOT NULL,
			revoked_at  timestamptz,
			created_at  timestamptz NOT NULL DEFAULT now()
		)`,
		// 0004：学工系统（xsgz）完整档案的四张表。
		// 与 migrations/0004_xsgz_student_profile.sql **逐列同构**（列名/类型/默认值），
		// 由 TestTestSchemaDDL_MatchesMigrationsStructurally 守住。
		`CREATE TABLE IF NOT EXISTS student_profiles (
			student_no          char(9)     PRIMARY KEY,
			one_card_no         text        NOT NULL DEFAULT '',
			student_name        text        NOT NULL DEFAULT '',
			sex                 text        NOT NULL DEFAULT '',
			sex_name            text        NOT NULL DEFAULT '',
			nationality         text        NOT NULL DEFAULT '',
			nationality_name    text        NOT NULL DEFAULT '',
			id_card_sealed      bytea,
			id_card_suffix4     char(4),
			birthday            text        NOT NULL DEFAULT '',
			photo               text        NOT NULL DEFAULT '',
			college_no          text        NOT NULL DEFAULT '',
			college_name        text        NOT NULL DEFAULT '',
			specialty_no        text        NOT NULL DEFAULT '',
			specialty_name      text        NOT NULL DEFAULT '',
			grade               text        NOT NULL DEFAULT '',
			class_no            text        NOT NULL DEFAULT '',
			class_name          text        NOT NULL DEFAULT '',
			campus_name         text        NOT NULL DEFAULT '',
			dorm_address        text        NOT NULL DEFAULT '',
			study_years         text        NOT NULL DEFAULT '',
			in_school_date      text        NOT NULL DEFAULT '',
			student_state_name  text        NOT NULL DEFAULT '',
			current_state_name  text        NOT NULL DEFAULT '',
			instructors         jsonb       NOT NULL DEFAULT '[]'::jsonb,
			examinee_no         text        NOT NULL DEFAULT '',
			high_school         text        NOT NULL DEFAULT '',
			source_location     text        NOT NULL DEFAULT '',
			email               text        NOT NULL DEFAULT '',
			mobile_phone        text        NOT NULL DEFAULT '',
			wechat              text        NOT NULL DEFAULT '',
			qq                  text        NOT NULL DEFAULT '',
			emergency_contact   text        NOT NULL DEFAULT '',
			emergency_relationship text     NOT NULL DEFAULT '',
			emergency_telephone text        NOT NULL DEFAULT '',
			emergency_address   text        NOT NULL DEFAULT '',
			family_address      text        NOT NULL DEFAULT '',
			family_detail_address text      NOT NULL DEFAULT '',
			family_telephone    text        NOT NULL DEFAULT '',
			family_postal_no    text        NOT NULL DEFAULT '',
			name_pinyin         text        NOT NULL DEFAULT '',
			blood_type_name     text        NOT NULL DEFAULT '',
			height              text        NOT NULL DEFAULT '',
			weight              text        NOT NULL DEFAULT '',
			native_place        text        NOT NULL DEFAULT '',
			nickname            text        NOT NULL DEFAULT '',
			avatar_url          text        NOT NULL DEFAULT '',
			age                 smallint,
			synced_at           timestamptz NOT NULL DEFAULT now(),
			updated_at          timestamptz NOT NULL DEFAULT now()
		)`,
		`CREATE TABLE IF NOT EXISTS student_profile_groups (
			student_no  char(9)     NOT NULL REFERENCES student_profiles (student_no) ON DELETE CASCADE,
			group_key   text        NOT NULL,
			payload     jsonb       NOT NULL,
			synced_at   timestamptz NOT NULL DEFAULT now(),
			PRIMARY KEY (student_no, group_key)
		)`,
		`CREATE TABLE IF NOT EXISTS student_family_members (
			student_no      char(9)     NOT NULL REFERENCES student_profiles (student_no) ON DELETE CASCADE,
			ordinal         smallint    NOT NULL,
			uuid            text        NOT NULL DEFAULT '',
			name            text        NOT NULL DEFAULT '',
			relationship    text        NOT NULL DEFAULT '',
			relationship_name text      NOT NULL DEFAULT '',
			unit            text        NOT NULL DEFAULT '',
			occupation      text        NOT NULL DEFAULT '',
			contact_number  text        NOT NULL DEFAULT '',
			is_guarder      text        NOT NULL DEFAULT '',
			payload         jsonb       NOT NULL,
			PRIMARY KEY (student_no, ordinal)
		)`,
		`CREATE TABLE IF NOT EXISTS student_resumes (
			student_no  char(9)     NOT NULL REFERENCES student_profiles (student_no) ON DELETE CASCADE,
			ordinal     smallint    NOT NULL,
			uuid        text        NOT NULL DEFAULT '',
			begin_date  text        NOT NULL DEFAULT '',
			end_date    text        NOT NULL DEFAULT '',
			study_unit  text        NOT NULL DEFAULT '',
			position    text        NOT NULL DEFAULT '',
			award       text        NOT NULL DEFAULT '',
			payload     jsonb       NOT NULL,
			PRIMARY KEY (student_no, ordinal)
		)`,
		// 0005/0006：内容域与消息/埋点表。逐列照 migrations/0005_content.sql
		// 与 0006_message_track.sql 的定义与顺序，供 DDL 一致性守卫比对。
		`CREATE TABLE IF NOT EXISTS announcements (
			id                 bigserial   PRIMARY KEY,
			title              text        NOT NULL,
			body               text        NOT NULL,
			status             text        NOT NULL DEFAULT 'draft'
			                   CHECK (status IN ('draft', 'published', 'revoked')),
			published_at       timestamptz,
			effective_from     timestamptz,
			expires_at         timestamptz,
			pinned             boolean     NOT NULL DEFAULT false,
			publish_revision   integer     NOT NULL DEFAULT 0,
			audience_scope     text        NOT NULL DEFAULT 'all'
			                   CHECK (audience_scope IN ('all', 'role', 'college_grade')),
			audience_roles     integer[]   NOT NULL DEFAULT '{}',
			audience_college   text        NOT NULL DEFAULT '',
			audience_grade     text        NOT NULL DEFAULT '',
			created_at         timestamptz NOT NULL DEFAULT now(),
			updated_at         timestamptz NOT NULL DEFAULT now(),
			deleted_at         timestamptz,
			CONSTRAINT announcements_audience_role_needs_roles
			    CHECK (audience_scope <> 'role' OR cardinality(audience_roles) > 0),
			CONSTRAINT announcements_audience_college_grade_needs_a_dimension
			    CHECK (audience_scope <> 'college_grade'
			           OR audience_college <> '' OR audience_grade <> ''),
			CONSTRAINT announcements_audience_all_has_no_conditions
			    CHECK (audience_scope <> 'all'
			           OR (cardinality(audience_roles) = 0
			               AND audience_college = '' AND audience_grade = '')),
			CONSTRAINT announcements_expiry_after_effective
			    CHECK (effective_from IS NULL OR expires_at IS NULL
			           OR expires_at > effective_from),
			CONSTRAINT announcements_published_needs_published_at
			    CHECK (status <> 'published' OR published_at IS NOT NULL)
		)`,
		`CREATE TABLE IF NOT EXISTS posts (
			id           bigserial   PRIMARY KEY,
			board        text        NOT NULL CHECK (board IN ('feedback', 'courtyard')),
			title        text        NOT NULL,
			body         text        NOT NULL,
			attachments  text[]      NOT NULL DEFAULT '{}',
			author_id    bigint      NOT NULL,
			published_at timestamptz NOT NULL DEFAULT now(),
			edited_at    timestamptz,
			created_at   timestamptz NOT NULL DEFAULT now(),
			updated_at   timestamptz NOT NULL DEFAULT now(),
			deleted_at   timestamptz
		)`,
		`CREATE TABLE IF NOT EXISTS post_replies (
			id           bigserial   PRIMARY KEY,
			post_id      bigint      NOT NULL REFERENCES posts (id),
			body         text        NOT NULL,
			author_id    bigint      NOT NULL,
			published_at timestamptz NOT NULL DEFAULT now(),
			created_at   timestamptz NOT NULL DEFAULT now(),
			deleted_at   timestamptz
		)`,
		`CREATE TABLE IF NOT EXISTS post_reactions (
			id          bigserial   PRIMARY KEY,
			target_type text   NOT NULL CHECK (target_type IN ('post', 'comment')),
			target_id   bigint NOT NULL,
			reaction    text   NOT NULL CHECK (reaction IN ('like', 'favorite')),
			user_id     bigint NOT NULL,
			created_at  timestamptz NOT NULL DEFAULT now()
		)`,
		`CREATE TABLE IF NOT EXISTS messages (
			id               bigserial   PRIMARY KEY,
			recipient_id     bigint      NOT NULL,
			kind             text        NOT NULL
			                 CHECK (kind IN ('notification', 'internal_mail')),
			source_domain    text        NOT NULL DEFAULT '',
			source_object_id text        NOT NULL DEFAULT '',
			title            text        NOT NULL DEFAULT '',
			body             text        NOT NULL DEFAULT '',
			hits             integer     NOT NULL DEFAULT 1,
			sender_id        bigint,
			read_at          timestamptz,
			deleted_at       timestamptz,
			created_at       timestamptz NOT NULL DEFAULT now(),
			CONSTRAINT messages_notification_needs_source CHECK (
			    kind <> 'notification'
			    OR (source_domain <> '' AND source_object_id <> '')
			)
		)`,
		`CREATE TABLE IF NOT EXISTS track_event (
			id           bigserial   PRIMARY KEY,
			event_id     text        NOT NULL,
			event_type   text        NOT NULL
			             CHECK (event_type IN (
			                 'screen_view', 'button_click', 'perf',
			                 'app_open', 'heartbeat', 'crash', 'llm_proxy_fail'
			             )),
			event_name   text        NOT NULL DEFAULT '',
			client_ts_ms bigint      NOT NULL DEFAULT 0,
			session_id   text        NOT NULL DEFAULT '',
			app_version  text        NOT NULL DEFAULT '',
			platform     text        NOT NULL
			             CHECK (platform IN ('ios', 'android', 'web')),
			props        jsonb,
			request_id   text        NOT NULL DEFAULT '',
			user_id      bigint      NOT NULL,
			received_at  timestamptz NOT NULL DEFAULT now()
		)`,
		// 0007：皮肤包上传/下载域（元数据 + 全局单指针）。
		// 逐列照 migrations/0007_skins.sql 的定义与顺序，供 DDL 一致性守卫比对。
		// 注意：迁移里额外的 skin_packs_created_at_idx 属「已知差异」（测试隔离
		// schema 不建二级索引），守卫只提示不判失败。
		`CREATE TABLE IF NOT EXISTS skin_packs (
			id              BIGSERIAL   PRIMARY KEY,
			pack_id         TEXT        NOT NULL,
			version         TEXT        NOT NULL,
			schema_version  INTEGER     NOT NULL,
			min_app_version INTEGER     NOT NULL,
			display_name    TEXT        NOT NULL,
			object_key      TEXT        NOT NULL,
			pack_sha256     TEXT        NOT NULL,
			pack_size       BIGINT      NOT NULL,
			manifest_json   TEXT        NOT NULL,
			created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
			created_by      TEXT        NOT NULL DEFAULT '',
			CONSTRAINT skin_packs_ref_unique UNIQUE (pack_id, version)
		)`,
		`CREATE TABLE IF NOT EXISTS skin_current (
			id          INTEGER     PRIMARY KEY DEFAULT 1 CHECK (id = 1),
			pack_id     TEXT        NOT NULL,
			version     TEXT        NOT NULL,
			updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
			CONSTRAINT skin_current_ref_fk
				FOREIGN KEY (pack_id, version)
				REFERENCES skin_packs (pack_id, version)
				ON DELETE RESTRICT
		)`,
		// 0008：教务「取数账号」池（公共服务专用）。
		// 逐列照 migrations/0008_fetch_accounts.sql 的定义与顺序。
		//
		// 本表曾长期缺失（2026-11 结构整改前）：DDL 一致性守卫只比对「两边都有的表」，
		// 所以漏掉整张表时守卫依然是绿的，但任何走 pgstore.FetchAccountPGRepo 的
		// 集成测试都会因为 relation 不存在而失败。守卫已补「表名集合必须相等」的断言。
		`CREATE TABLE IF NOT EXISTS jiaowu_fetch_accounts (
			id                BIGSERIAL   PRIMARY KEY,
			label             TEXT        NOT NULL DEFAULT '',
			tgt_sealed        BYTEA       NOT NULL,
			vpn_ticket_sealed BYTEA       NOT NULL,
			enabled           BOOLEAN     NOT NULL DEFAULT TRUE,
			failure_count     INTEGER     NOT NULL DEFAULT 0,
			last_used_at      TIMESTAMPTZ,
			created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
	}
}
