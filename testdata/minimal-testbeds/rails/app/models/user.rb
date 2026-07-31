class User < ActiveRecord::Base
  has_many :posts
  belongs_to :account
end
