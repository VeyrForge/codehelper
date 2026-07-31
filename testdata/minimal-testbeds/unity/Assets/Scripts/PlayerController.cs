using UnityEngine;

namespace Example.UnityLite
{
    [RequireComponent(typeof(Health))]
    public class PlayerController : MonoBehaviour
    {
        public Rigidbody Body;
        public Health Hp;

        public void Awake()
        {
            Body = GetComponent<Rigidbody>();
            Hp = GetComponent<Health>();
            var bar = FindObjectOfType<HealthBar>();
            _ = bar;
        }

        public void Move(Vector3 delta)
        {
            if (Body != null)
            {
                Body.position += delta;
            }
        }
    }

    public class HealthBar : MonoBehaviour
    {
    }
}
