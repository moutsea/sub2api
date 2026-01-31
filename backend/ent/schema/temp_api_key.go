package schema

import (
	"github.com/Wei-Shaw/sub2api/ent/schema/mixins"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// TempAPIKey holds the schema definition for the TempAPIKey entity.
// 临时 API Key，支持有效期和每日请求限制
type TempAPIKey struct {
	ent.Schema
}

// Annotations of the TempAPIKey.
func (TempAPIKey) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "temp_api_keys"},
	}
}

// Mixin of the TempAPIKey.
func (TempAPIKey) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixins.TimeMixin{},
		mixins.SoftDeleteMixin{},
	}
}

// Fields of the TempAPIKey.
func (TempAPIKey) Fields() []ent.Field {
	return []ent.Field{
		// Key 字段
		field.String("key").
			MaxLen(128).
			Unique().
			Comment("API Key，sk-temp-xxx 格式"),
		field.String("name").
			MaxLen(100).
			Comment("名称/备注"),
		field.Int64("group_id").
			Comment("关联分组 ID"),

		// Key 类型：time_limited（限时+限额）、quota_only（仅限额不限时）
		field.String("key_type").
			Default("time_limited").
			Comment("类型：time_limited（限时限额）、quota_only（仅限额不限时）"),

		// 总额度限制（仅 quota_only 类型使用，单位：美元）
		field.Float("total_quota_usd").
			Default(0).
			Comment("总额度限制（美元），0 表示不限制，仅 quota_only 类型使用"),

		// 已消费金额（仅 quota_only 类型使用，单位：美元）
		field.Float("total_cost_usd").
			Default(0).
			Comment("已消费金额（美元），仅 quota_only 类型使用"),

		// 有效期设置
		field.Int("valid_days").
			Default(7).
			Comment("有效天数，从首次使用开始计算"),
		field.Time("activated_at").
			Optional().
			Nillable().
			Comment("首次激活时间"),
		field.Time("expires_at").
			Optional().
			Nillable().
			Comment("过期时间"),

		// 请求限制
		field.Int("daily_limit").
			Default(1000).
			Comment("每 24 小时请求限制"),

		// 当前周期使用统计
		field.Time("current_period_start").
			Optional().
			Nillable().
			Comment("当前 24 小时周期开始时间"),
		field.Int("current_period_count").
			Default(0).
			Comment("当前周期请求次数"),

		// 总计统计
		field.Int64("total_requests").
			Default(0).
			Comment("总请求次数"),

		// 状态
		field.String("status").
			Default("active").
			Comment("状态：active, inactive, expired"),

		// 创建者
		field.Int64("created_by").
			Comment("创建者 ID（管理员）"),
	}
}

// Edges of the TempAPIKey.
func (TempAPIKey) Edges() []ent.Edge {
	return []ent.Edge{
		// 关联分组
		edge.From("group", Group.Type).
			Ref("temp_api_keys").
			Field("group_id").
			Unique().
			Required(),
		// 创建者
		edge.From("creator", User.Type).
			Ref("created_temp_api_keys").
			Field("created_by").
			Unique().
			Required(),
		// 使用日志
		edge.To("usage_logs", UsageLog.Type),
	}
}

// Indexes of the TempAPIKey.
func (TempAPIKey) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("key").
			Unique(),
		index.Fields("group_id"),
		index.Fields("status"),
		index.Fields("created_by"),
		index.Fields("expires_at"),
	}
}
