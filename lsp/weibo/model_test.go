package weibo

import (
	"github.com/Oumainory/DDBOT-AI/internal/test"
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestUserInfo(t *testing.T) {
	userInfo := &UserInfo{
		Uid:             test.UID1,
		Name:            test.NAME1,
		ProfileImageUrl: test.FakeImage(10),
		ProfileUrl:      test.FakeImage(20),
	}
	assert.EqualValues(t, test.UID1, userInfo.GetUid())
	assert.EqualValues(t, test.NAME1, userInfo.GetName())
	assert.NotNil(t, userInfo.Logger())
	assert.EqualValues(t, Site, userInfo.Site())
}

func TestNewsInfo(t *testing.T) {
	userInfo := &UserInfo{
		Uid:             test.UID1,
		Name:            test.NAME1,
		ProfileImageUrl: test.FakeImage(10),
		ProfileUrl:      test.FakeImage(20),
	}
	newsInfo := &NewsInfo{
		UserInfo:     userInfo,
		LatestNewsTs: test.DynamicID1,
		Cards: []*Card{
			{
				Mblogtype: CardType_Normal,
				RawText:   "raw",
			},
			{
				Mblogtype: CardType_Normal,
				PicInfos: map[string]*Card_PicInfo{
					"testpic": {
						Large: &Card_PicVariant{
							Url: test.FakeImage(10),
						},
					},
				},
				RetweetedStatus: &Card{
					User: &ApiContainerGetIndexProfileResponse_Data_UserInfo{
						ScreenName: test.NAME2,
					},
				},
			},
		},
	}
	assert.EqualValues(t, News, newsInfo.Type())
	assert.NotNil(t, newsInfo.Logger())

	concernNews := NewConcernNewsNotify(test.G1, newsInfo)
	assert.NotNil(t, concernNews)
	assert.Len(t, concernNews, len(newsInfo.Cards))

	for _, concernNewsNotify := range concernNews {
		assert.EqualValues(t, News, concernNewsNotify.Type())
		assert.EqualValues(t, test.G1, concernNewsNotify.GetGroupCode())
		assert.NotNil(t, concernNewsNotify.Logger())
		assert.NotNil(t, concernNewsNotify.ToMessage())
	}
}
